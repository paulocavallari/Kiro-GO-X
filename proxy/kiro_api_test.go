package proxy

import (
	"io"
	"kiro-go-x/config"
	accountpool "kiro-go-x/pool"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveProfileArnReturnsCachedValueWithoutRequest(t *testing.T) {
	kiroRestHttpStore.Store(&http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("unexpected HTTP request for cached profile ARN")
			return nil, nil
		}),
	})
	t.Cleanup(func() { InitKiroHttpClient("") })

	account := &config.Account{ProfileArn: " arn:aws:codewhisperer:profile/test "}
	got, err := ResolveProfileArn(account)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "arn:aws:codewhisperer:profile/test" {
		t.Fatalf("expected trimmed cached ARN, got %q", got)
	}
}

func TestResolveProfileArnFetchesAndCachesProfile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.Init(configPath); err != nil {
		t.Fatalf("init config: %v", err)
	}
	account := config.Account{
		ID:           "acct-1",
		Email:        "user@example.com",
		AccessToken:  "access-token",
		Region:       "us-east-1",
		UsageCurrent: 7,
	}
	if err := config.AddAccount(account); err != nil {
		t.Fatalf("add account: %v", err)
	}

	kiroRestHttpStore.Store(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Fatalf("expected POST, got %s", req.Method)
			}
			if req.URL.Path != "/ListAvailableProfiles" {
				t.Fatalf("expected ListAvailableProfiles path, got %s", req.URL.Path)
			}
			if got := req.Header.Get("Content-Type"); got != "application/json" {
				t.Fatalf("expected JSON content type, got %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"profiles":[{"arn":" arn:aws:codewhisperer:profile/fetched "}]} `)),
				Header:     make(http.Header),
			}, nil
		}),
	})
	t.Cleanup(func() { InitKiroHttpClient("") })

	requestAccount := account
	requestAccount.UsageCurrent = 0
	got, err := ResolveProfileArn(&requestAccount)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "arn:aws:codewhisperer:profile/fetched" {
		t.Fatalf("expected fetched ARN, got %q", got)
	}
	if requestAccount.ProfileArn != got {
		t.Fatalf("expected account to be updated with fetched ARN, got %q", requestAccount.ProfileArn)
	}

	accounts := config.GetAccounts()
	if len(accounts) != 1 {
		t.Fatalf("expected one persisted account, got %d", len(accounts))
	}
	if accounts[0].ProfileArn != got {
		t.Fatalf("expected persisted account profile ARN %q, got %q", got, accounts[0].ProfileArn)
	}
	if accounts[0].UsageCurrent != 7 {
		t.Fatalf("expected profile cache update to preserve usage fields, got usageCurrent=%v", accounts[0].UsageCurrent)
	}
}

func TestResolveProfileArnSynchronizesCachedValueToPoolSnapshots(t *testing.T) {
	if err := config.Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("init config: %v", err)
	}
	account := config.Account{
		ID: "pool-profile-cache", Provider: "Enterprise", AuthMethod: "idc",
		AccessToken: "access-token", Region: "us-east-1", Enabled: true,
	}
	if err := config.AddAccount(account); err != nil {
		t.Fatalf("add account: %v", err)
	}
	p := accountpool.GetPool()
	p.ResetRuntimeState()
	p.Reload()

	calls := 0
	kiroRestHttpStore.Store(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Path != "/ListAvailableProfiles" {
			t.Fatalf("unexpected request path: %s", req.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"profiles":[{"arn":"arn:cached-in-pool"}]}`)),
			Header:     make(http.Header),
		}, nil
	})})
	t.Cleanup(func() { InitKiroHttpClient("") })

	firstSnapshot := p.GetByID(account.ID)
	if firstSnapshot == nil {
		t.Fatal("account missing from pool")
	}
	got, err := ResolveProfileArn(firstSnapshot)
	if err != nil || got != "arn:cached-in-pool" {
		t.Fatalf("first profile resolution = %q, %v", got, err)
	}
	if pooled := p.GetByID(account.ID); pooled == nil || pooled.ProfileArn != got {
		t.Fatalf("pool snapshot profile ARN = %#v, want %q", pooled, got)
	}

	secondSnapshot := p.GetByID(account.ID)
	got, err = ResolveProfileArn(secondSnapshot)
	if err != nil || got != "arn:cached-in-pool" {
		t.Fatalf("second profile resolution = %q, %v", got, err)
	}
	if calls != 1 {
		t.Fatalf("ListAvailableProfiles calls = %d, want 1", calls)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestUsageLimitsUsesBuilderIDFallbackWithoutChangingOtherAccounts(t *testing.T) {
	if err := config.Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		account config.Account
		wantARN string
	}{
		{"builder without ARN", config.Account{Provider: "BuilderId", AuthMethod: "idc"}, builderIDDefaultProfileArn},
		{"builder with explicit ARN", config.Account{Provider: "BuilderId", AuthMethod: "idc", ProfileArn: " explicit-arn "}, "explicit-arn"},
		{"enterprise without ARN", config.Account{Provider: "Enterprise", AuthMethod: "idc"}, ""},
		{"enterprise with ARN", config.Account{Provider: "Enterprise", AuthMethod: "idc", ProfileArn: "enterprise-arn"}, "enterprise-arn"},
		{"social without ARN", config.Account{Provider: "Google", AuthMethod: "social"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kiroRestHttpStore.Store(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path != "/getUsageLimits" {
					t.Fatalf("unexpected path: %s", req.URL.Path)
				}
				if got := req.URL.Query().Get("profileArn"); got != tc.wantARN {
					t.Errorf("profileArn = %q, want %q", got, tc.wantARN)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"usageBreakdownList":[]}`)), Header: make(http.Header)}, nil
			})})
			if _, err := GetUsageLimits(&tc.account); err != nil {
				t.Fatalf("GetUsageLimits: %v", err)
			}
		})
	}
	t.Cleanup(func() { InitKiroHttpClient("") })
}

func TestResolveProfileArnRefreshFallbackUsesSerializedRefresh(t *testing.T) {
	if err := config.Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatal(err)
	}
	account := config.Account{
		ID:           "profile-refresh-account",
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		AuthMethod:   "idc",
		Provider:     "Enterprise",
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		Region:       "us-east-1",
		ExpiresAt:    1,
		Enabled:      false,
	}
	if err := config.AddAccount(account); err != nil {
		t.Fatal(err)
	}
	p := accountpool.GetPool()
	p.ResetRuntimeState()
	p.Reload()
	kiroRestHttpStore.Store(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/ListAvailableProfiles" {
			t.Fatalf("unexpected path: %s", req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})})
	t.Cleanup(func() { InitKiroHttpClient("") })

	var calls int
	refresh := func(a *config.Account) (string, error) {
		err := refreshAccountTokenSerialized(p, a, true, func(*config.Account) (string, string, int64, string, error) {
			calls++
			return "new-access", "new-refresh", time.Now().Add(time.Hour).Unix(), "enterprise-profile", nil
		})
		return a.ProfileArn, err
	}
	got, err := resolveProfileArnWithRefresh(&account, refresh)
	if err != nil {
		t.Fatalf("resolveProfileArnWithRefresh: %v", err)
	}
	if got != "enterprise-profile" || calls != 1 {
		t.Fatalf("got ARN %q and %d refresh calls, want enterprise-profile and 1", got, calls)
	}
	stored, ok := config.GetAccount(account.ID)
	if !ok || stored.AccessToken != "new-access" || stored.RefreshToken != "new-refresh" || stored.ProfileArn != "enterprise-profile" {
		t.Fatalf("refresh fallback did not persist credentials and ARN: %#v", stored)
	}
	if got := p.GetByID(account.ID); got != nil {
		t.Fatalf("disabled account unexpectedly entered pool: %#v", got)
	}
}

func TestBuilderIDProfileFallback(t *testing.T) {
	if err := config.Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatal(err)
	}
	kiroRestHttpStore.Store(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"profiles":[]}`)), Header: make(http.Header)}, nil
	})})
	t.Cleanup(func() { InitKiroHttpClient("") })
	const fallback = "arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX"
	cases := []struct {
		name    string
		account *config.Account
		want    string
	}{
		{"builder", &config.Account{Provider: "BuilderId", AuthMethod: "idc"}, fallback},
		{"builder normalized", &config.Account{Provider: " builderid ", AuthMethod: " IDC "}, fallback},
		{"explicit builder", &config.Account{Provider: "BuilderId", AuthMethod: "idc", ProfileArn: " explicit "}, "explicit"},
		{"explicit enterprise", &config.Account{Provider: "Enterprise", AuthMethod: "idc", ProfileArn: " enterprise-arn "}, "enterprise-arn"},
		{"enterprise idc", &config.Account{Provider: "Enterprise", AuthMethod: "idc"}, ""},
		{"unidentified idc", &config.Account{AuthMethod: "idc"}, ""},
		{"social", &config.Account{Provider: "Google", AuthMethod: "social"}, ""},
		{"contradictory social", &config.Account{Provider: "BuilderId", AuthMethod: "social"}, ""},
		{"unknown method", &config.Account{Provider: "BuilderId"}, ""},
		{"nil", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := "https://example.com/path?origin=test"
			u, err := url.Parse(withProfileArnQuery(raw, tc.account))
			if err != nil {
				t.Fatal(err)
			}
			if got := u.Query().Get("profileArn"); got != tc.want {
				t.Errorf("query profile ARN = %q, want %q", got, tc.want)
			}
			if tc.account != nil {
				got, err := ResolveProfileArn(tc.account)
				if got != tc.want || (tc.want == "") != (err != nil) {
					t.Errorf("resolve = %q, %v; want %q", got, err, tc.want)
				}
			}
		})
	}
}
