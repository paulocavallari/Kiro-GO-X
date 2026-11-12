package proxy

import (
	"io"
	"kiro-go-x/config"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
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
			wantQuery := ""
			if tc.account != nil {
				wantQuery = strings.TrimSpace(tc.account.ProfileArn)
			}
			if got := u.Query().Get("profileArn"); got != wantQuery {
				t.Errorf("query profile ARN = %q, want %q", got, wantQuery)
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
