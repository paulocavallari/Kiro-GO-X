package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"kiro-go-x/config"
	accountpool "kiro-go-x/pool"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBonusRefreshPersistenceAndAPI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Init(path); err != nil {
		t.Fatal(err)
	}
	if err := config.AddAccount(config.Account{ID: "bonus-test", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	body := `{"usageBreakdownList":[{"currentUsage":10,"usageLimit":100,"bonuses":[{"status":"ACTIVE","currentUsage":22,"currentUsageWithPrecision":22.87,"usageLimit":500,"usageLimitWithPrecision":500},{"status":"EXPIRED","currentUsage":10,"usageLimit":20},{"status":"ACTIVE","expiresAt":1,"currentUsage":1,"usageLimit":10}]}]}`
	kiroRestHttpStore.Store(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/getUsageLimits" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})})
	t.Cleanup(func() { InitKiroHttpClient("") })
	refresh := func() {
		t.Helper()
		info, err := RefreshAccountInfo(&config.Account{ID: "bonus-test"})
		if err != nil {
			t.Fatal(err)
		}
		if err = config.UpdateAccountInfo("bonus-test", *info); err != nil {
			t.Fatal(err)
		}
	}
	refresh()
	if err := config.Load(); err != nil {
		t.Fatal(err)
	}
	h := &Handler{pool: accountpool.GetPool()}
	h.pool.Reload()
	check := func(known bool, current, limit float64) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/admin/api/accounts", nil)
		req.Header.Set("X-Admin-Password", config.GetPassword())
		h.handleAdminAPI(rec, req)
		var accounts []map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &accounts); err != nil {
			t.Fatalf("%s: %v", rec.Body.String(), err)
		}
		if len(accounts) != 1 {
			t.Fatalf("accounts: %v", accounts)
		}
		a := accounts[0]
		if a["bonusUsageKnown"] != known || a["bonusUsageCurrent"] != current || a["bonusUsageLimit"] != limit {
			t.Fatalf("unexpected bonus: %v", a)
		}
	}
	check(true, 22.87, 500)
	// The public admin refresh route also fetches and saves the bonus snapshot.
	refreshRec := httptest.NewRecorder()
	refreshReq := httptest.NewRequest("POST", "/admin/api/accounts/bonus-test/refresh", nil)
	refreshReq.Header.Set("X-Admin-Password", config.GetPassword())
	h.handleAdminAPI(refreshRec, refreshReq)
	if refreshRec.Code != 200 {
		t.Fatalf("refresh status %d: %s", refreshRec.Code, refreshRec.Body.String())
	}
	var refreshResult struct {
		Info map[string]interface{} `json:"info"`
	}
	if err := json.Unmarshal(refreshRec.Body.Bytes(), &refreshResult); err != nil {
		t.Fatal(err)
	}
	if refreshResult.Info["bonusUsageCurrent"] != 22.87 {
		t.Fatalf("refresh response: %s", refreshRec.Body.String())
	}
	check(true, 22.87, 500)
	// A failed admin refresh must not overwrite the last known balance.
	kiroRestHttpStore.Store(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("upstream failure")), Header: make(http.Header)}, nil
	})})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/admin/api/accounts/bonus-test/refresh", nil)
	req.Header.Set("X-Admin-Password", config.GetPassword())
	h.handleAdminAPI(rec, req)
	if rec.Code != 500 {
		t.Fatalf("refresh status %d: %s", rec.Code, rec.Body.String())
	}
	if err := config.Load(); err != nil {
		t.Fatal(err)
	}
	check(true, 22.87, 500)
	kiroRestHttpStore.Store(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})})
	body = `{"usageBreakdownList":[{"bonuses":[]}]}`
	refresh()
	check(true, 0, 0)
	body = `{"usageBreakdownList":[{}]}`
	refresh()
	check(false, 0, 0)
}

func TestBonusRefreshAggregatesActiveAndRejectsIncomplete(t *testing.T) {
	cases := []struct {
		name, body     string
		known          bool
		current, limit float64
	}{
		{"multiple", `{"usageBreakdownList":[{"bonuses":[{"status":"ACTIVE","currentUsage":2.5,"usageLimit":10},{"status":"ACTIVE","currentUsage":1,"usageLimit":5}]}]}`, true, 3.5, 15},
		{"inactive", `{"usageBreakdownList":[{"bonuses":[{"status":"EXPIRED","currentUsage":2,"usageLimit":10}]}]}`, true, 0, 0},
		{"missing values", `{"usageBreakdownList":[{"bonuses":[{"status":"ACTIVE","usageLimit":10}]}]}`, false, 0, 0},
		{"no breakdown", `{}`, false, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kiroRestHttpStore.Store(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})})
			defer InitKiroHttpClient("")
			info, err := RefreshAccountInfo(&config.Account{})
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(info)
			var m map[string]interface{}
			_ = json.Unmarshal(b, &m)
			if m["bonusUsageKnown"] != tc.known || m["bonusUsageCurrent"] != tc.current || m["bonusUsageLimit"] != tc.limit {
				t.Fatalf("bonus info: %s", b)
			}
		})
	}
}

func TestBonusUnknownStatusAndMillisecondExpiry(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name, entry string
		known       bool
		limit       float64
	}{
		{"missing status", `{"currentUsage":1,"usageLimit":10}`, false, 0},
		{"unknown status", `{"status":"FUTURE","currentUsage":1,"usageLimit":10}`, false, 0},
		{"expired milliseconds", fmt.Sprintf(`{"status":"ACTIVE","expiresAt":%d,"currentUsage":1,"usageLimit":10}`, now.Add(-time.Hour).UnixMilli()), true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b []UsageBreakdown
			if err := json.Unmarshal([]byte(`[{"bonuses":[`+tc.entry+`]}]`), &b); err != nil {
				t.Fatal(err)
			}
			known, _, limit := bonusUsage(b, now)
			if known != tc.known || limit != tc.limit {
				t.Fatalf("got known=%v limit=%v", known, limit)
			}
		})
	}
}

func TestBonusExpiryPersistsAndGETInvalidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Init(path); err != nil {
		t.Fatal(err)
	}
	if err := config.AddAccount(config.Account{ID: "expiring", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(1500 * time.Millisecond).UnixMilli()
	body := fmt.Sprintf(`{"usageBreakdownList":[{"bonuses":[{"status":"ACTIVE","expiresAt":%d,"currentUsage":1,"usageLimit":10},{"status":"ACTIVE","expiresAt":%d,"currentUsage":2,"usageLimit":20}]}]}`, expiry, expiry+60000)
	kiroRestHttpStore.Store(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})})
	t.Cleanup(func() { InitKiroHttpClient("") })
	info, err := RefreshAccountInfo(&config.Account{ID: "expiring"})
	if err != nil {
		t.Fatal(err)
	}
	if err := config.UpdateAccountInfo("expiring", *info); err != nil {
		t.Fatal(err)
	}
	if err := config.Load(); err != nil {
		t.Fatal(err)
	}
	stored := config.GetAccounts()
	if len(stored) != 1 || !stored[0].BonusUsageExpiryChecked || stored[0].BonusUsageExpiresAt != expiry {
		t.Fatalf("expiry not persisted: %+v", stored)
	}
	h := &Handler{pool: accountpool.GetPool()}
	h.pool.Reload()
	check := func(want bool) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/admin/api/accounts", nil)
		req.Header.Set("X-Admin-Password", config.GetPassword())
		h.handleAdminAPI(rec, req)
		var accounts []map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &accounts); err != nil {
			t.Fatal(err)
		}
		if len(accounts) != 1 || accounts[0]["bonusUsageKnown"] != want {
			t.Fatalf("want known %v: %s", want, rec.Body.String())
		}
		if !want && (accounts[0]["bonusUsageCurrent"] != float64(0) || accounts[0]["bonusUsageLimit"] != float64(0)) {
			t.Fatalf("stale amounts exposed: %s", rec.Body.String())
		}
	}
	check(true)
	time.Sleep(time.Until(time.UnixMilli(expiry)) + 50*time.Millisecond)
	check(false)
}

func TestBonusExpirySecondsAndLegacySnapshot(t *testing.T) {
	now := time.Unix(1700000000, 0)
	var b []UsageBreakdown
	raw := `[{"bonuses":[{"status":"ACTIVE","expiresAt":1700000100,"currentUsage":1,"usageLimit":10}]}]`
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}
	known, _, limit, expiry := bonusUsageSnapshot(b, now)
	if !known || limit != 10 || expiry != 1700000100000 {
		t.Fatalf("unexpected snapshot: %v %v %v", known, limit, expiry)
	}
	a := bonusSnapshotForRead(config.Account{BonusUsageKnown: true, BonusUsageCurrent: 1, BonusUsageLimit: 10}, now)
	if a.BonusUsageKnown || a.BonusUsageCurrent != 0 || a.BonusUsageLimit != 0 {
		t.Fatalf("legacy snapshot exposed: %+v", a)
	}
}
