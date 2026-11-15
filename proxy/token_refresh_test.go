package proxy

import (
	"errors"
	"kiro-go-x/config"
	accountpool "kiro-go-x/pool"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefreshAccountTokenSerializesConcurrentRefreshes(t *testing.T) {
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	account := config.Account{
		ID:           "account-1",
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		ExpiresAt:    time.Now().Add(-time.Minute).Unix(),
		Enabled:      true,
	}
	if err := config.AddAccount(account); err != nil {
		t.Fatalf("config.AddAccount: %v", err)
	}
	p := accountpool.GetPool()
	p.ResetRuntimeState()
	p.Reload()
	h := &Handler{pool: p}

	var calls atomic.Int32
	refresh := func(*config.Account) (string, string, int64, string, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return "new-access", "new-refresh", time.Now().Add(time.Hour).Unix(), "new-profile", nil
	}

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stale := account
			errs <- h.refreshAccountTokenWith(&stale, false, refresh)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("refreshAccountTokenWith: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
	stored := config.GetAccounts()
	if len(stored) != 1 || stored[0].AccessToken != "new-access" || stored[0].RefreshToken != "new-refresh" {
		t.Fatalf("refreshed credentials were not persisted")
	}
	if got := p.GetByID(account.ID); got == nil || got.AccessToken != "new-access" || got.RefreshToken != "new-refresh" || got.ProfileArn != "new-profile" {
		t.Fatalf("refreshed credentials and profile ARN were not synchronized to pool")
	}
}

func TestRefreshAccountTokenSerializesDisabledAccountFromConfig(t *testing.T) {
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	account := config.Account{
		ID:           "disabled-account",
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		ExpiresAt:    time.Now().Add(-time.Minute).Unix(),
		Enabled:      false,
	}
	if err := config.AddAccount(account); err != nil {
		t.Fatalf("config.AddAccount: %v", err)
	}
	p := accountpool.GetPool()
	p.ResetRuntimeState()
	p.Reload()
	if p.GetByID(account.ID) != nil {
		t.Fatal("disabled account unexpectedly exists in pool")
	}
	h := &Handler{pool: p}

	var calls atomic.Int32
	refresh := func(*config.Account) (string, string, int64, string, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return "new-access", "new-refresh", time.Now().Add(time.Hour).Unix(), "new-profile", nil
	}

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stale := account
			errs <- h.refreshAccountTokenWith(&stale, false, refresh)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("refreshAccountTokenWith: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d for disabled stale account, want 1", got)
	}
	stored := config.GetAccounts()
	if len(stored) != 1 || stored[0].AccessToken != "new-access" || stored[0].RefreshToken != "new-refresh" || stored[0].ProfileArn != "new-profile" {
		t.Fatalf("refreshed disabled account was not persisted: %#v", stored)
	}
}

func TestRefreshFailureDoesNotReplayGrantAfterConfigReload(t *testing.T) {
	configPath := t.TempDir() + "/config.json"
	if err := config.Init(configPath); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	account := config.Account{
		ID: "ambiguous-account", AccessToken: "old-access", RefreshToken: "old-refresh",
		ClientID: "test-client", ClientSecret: "test-secret",
		ExpiresAt: 1, Enabled: true,
	}
	if err := config.AddAccount(account); err != nil {
		t.Fatalf("config.AddAccount: %v", err)
	}
	p := accountpool.GetPool()
	p.ResetRuntimeState()
	p.Reload()
	h := &Handler{pool: p}

	var calls atomic.Int32
	refresh := func(*config.Account) (string, string, int64, string, error) {
		calls.Add(1)
		return "", "", 0, "", errors.New("refresh failed: HTTP 500 server_error")
	}
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stale := account
			errs <- h.refreshAccountTokenWith(&stale, true, refresh)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err == nil {
			t.Fatal("expected each caller to observe the ambiguous refresh failure")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent refresh calls = %d, want 1", got)
	}
	if err := config.Init(configPath); err != nil {
		t.Fatalf("reload config: %v", err)
	}
	stale := account
	if err := h.refreshAccountTokenWith(&stale, true, refresh); err == nil {
		t.Fatal("expected unresolved refresh outcome to remain blocked")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("refresh calls after config reload = %d, want 1", got)
	}

	if err := config.UpdateAccountToken(account.ID, "reauth-access", "reauth-refresh", 1); err != nil {
		t.Fatalf("replace credentials after reauthentication: %v", err)
	}
	stale = account
	if err := h.refreshAccountTokenWith(&stale, true, func(*config.Account) (string, string, int64, string, error) {
		calls.Add(1)
		return "recovered-access", "recovered-refresh", time.Now().Add(time.Hour).Unix(), "", nil
	}); err != nil {
		t.Fatalf("refresh after reauthentication: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("refresh calls after new credentials = %d, want 2", got)
	}
}

func TestRefreshPersistenceFailureRetainsRotatedCredentialsForPersistenceRetry(t *testing.T) {
	configPath := t.TempDir() + "/config.json"
	if err := config.Init(configPath); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	account := config.Account{
		ID: "pending-account", AccessToken: "old-access", RefreshToken: "old-refresh",
		ClientID: "test-client", ClientSecret: "test-secret",
		ExpiresAt: 1, Enabled: true,
	}
	if err := config.AddAccount(account); err != nil {
		t.Fatalf("config.AddAccount: %v", err)
	}
	p := accountpool.GetPool()
	p.ResetRuntimeState()
	p.Reload()
	h := &Handler{pool: p}
	blockedPath := t.TempDir() + "/not-a-config-file"
	if err := os.Mkdir(blockedPath, 0700); err != nil {
		t.Fatalf("create blocked path: %v", err)
	}

	var calls atomic.Int32
	stale := account
	err := h.refreshAccountTokenWith(&stale, true, func(*config.Account) (string, string, int64, string, error) {
		calls.Add(1)
		if err := config.Init(blockedPath); err == nil {
			t.Fatal("config.Init on directory unexpectedly succeeded")
		}
		return "rotated-access", "rotated-refresh", time.Now().Add(time.Hour).Unix(), "rotated-profile", nil
	})
	if err == nil {
		t.Fatal("expected credential persistence to fail")
	}
	if err := config.Init(configPath); err != nil {
		t.Fatalf("restore config path: %v", err)
	}

	stale = account
	if err := h.refreshAccountTokenWith(&stale, true, func(*config.Account) (string, string, int64, string, error) {
		calls.Add(1)
		return "unexpected-access", "unexpected-refresh", 1, "", nil
	}); err != nil {
		t.Fatalf("persist pending credentials without another grant: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
	stored, ok := config.GetAccount(account.ID)
	if !ok || stored.AccessToken != "rotated-access" || stored.RefreshToken != "rotated-refresh" || stored.ProfileArn != "rotated-profile" {
		t.Fatalf("rotated credentials were not persisted: %#v", stored)
	}
	if got := p.GetByID(account.ID); got == nil || got.AccessToken != "rotated-access" || got.RefreshToken != "rotated-refresh" || got.ProfileArn != "rotated-profile" {
		t.Fatalf("pool was not synchronized with persisted rotated credentials: %#v", got)
	}
}

func TestInFlightRefreshDoesNotOverwriteSameTokenReauthentication(t *testing.T) {
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	account := config.Account{
		ID: "inflight-reauth", AccessToken: "old-access", RefreshToken: "old-refresh",
		ClientID: "test-client", ClientSecret: "test-secret", ExpiresAt: 1, Enabled: true,
	}
	if err := config.AddAccount(account); err != nil {
		t.Fatalf("config.AddAccount: %v", err)
	}
	p := accountpool.GetPool()
	p.ResetRuntimeState()
	p.Reload()
	h := &Handler{pool: p}
	defer delete(pendingRefreshByAccount, account.ID)

	var calls atomic.Int32
	stale := account
	err := h.refreshAccountTokenWith(&stale, true, func(*config.Account) (string, string, int64, string, error) {
		calls.Add(1)
		if err := config.UpdateAccountToken(account.ID, "reauth-access", "old-refresh", 5678); err != nil {
			t.Fatalf("install reauthenticated credentials during refresh: %v", err)
		}
		return "rotated-access", "rotated-refresh", 1234, "rotated-profile", nil
	})
	if err != config.ErrAccountCredentialsChanged {
		t.Fatalf("in-flight stale refresh error = %v, want ErrAccountCredentialsChanged", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh grant calls = %d, want 1", calls.Load())
	}
	stored, ok := config.GetAccount(account.ID)
	if !ok || stored.AccessToken != "reauth-access" || stored.RefreshToken != "old-refresh" || stored.ExpiresAt != 5678 || stored.RefreshTokenUncertain || stored.PendingRefresh != nil {
		t.Fatalf("in-flight refresh overwrote reauthenticated credentials: %#v", stored)
	}
}

func TestRefreshPendingMapDoesNotOverwriteReauthWithSameRefreshToken(t *testing.T) {
	configPath := t.TempDir() + "/config.json"
	if err := config.Init(configPath); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	account := config.Account{
		ID: "pending-reauth", AccessToken: "old-access", RefreshToken: "old-refresh",
		ClientID: "test-client", ClientSecret: "test-secret", ExpiresAt: 1, Enabled: true,
	}
	if err := config.AddAccount(account); err != nil {
		t.Fatalf("config.AddAccount: %v", err)
	}
	p := accountpool.GetPool()
	p.ResetRuntimeState()
	p.Reload()
	h := &Handler{pool: p}
	blockedPath := t.TempDir() + "/not-a-config-file"
	if err := os.Mkdir(blockedPath, 0700); err != nil {
		t.Fatalf("create blocked path: %v", err)
	}
	defer delete(pendingRefreshByAccount, account.ID)

	var calls atomic.Int32
	stale := account
	err := h.refreshAccountTokenWith(&stale, true, func(*config.Account) (string, string, int64, string, error) {
		calls.Add(1)
		if err := config.Init(blockedPath); err == nil {
			t.Fatal("config.Init on directory unexpectedly succeeded")
		}
		return "rotated-access", "rotated-refresh", time.Now().Add(time.Hour).Unix(), "rotated-profile", nil
	})
	if err == nil {
		t.Fatal("expected persistence failure to leave an in-memory pending rotation")
	}
	if err := config.Init(configPath); err != nil {
		t.Fatalf("restore config path: %v", err)
	}
	if err := config.UpdateAccountToken(account.ID, "reauth-access", "old-refresh", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatalf("replace credentials while retaining the same refresh token: %v", err)
	}

	stale = account
	if err := h.refreshAccountTokenWith(&stale, true, func(*config.Account) (string, string, int64, string, error) {
		calls.Add(1)
		return "unexpected-access", "unexpected-refresh", 1, "", nil
	}); err != nil {
		t.Fatalf("use reauthenticated credentials: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh grant calls = %d, want only the original call", calls.Load())
	}
	stored, ok := config.GetAccount(account.ID)
	if !ok || stored.AccessToken != "reauth-access" || stored.RefreshToken != "old-refresh" || stored.RefreshTokenUncertain || stored.PendingRefresh != nil {
		t.Fatalf("stale pending rotation overwrote reauthenticated credentials: %#v", stored)
	}
}

func TestForcedRefreshIsNotSkippedWhenOnlyProfileArnChanged(t *testing.T) {
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	account := config.Account{
		ID: "forced-profile-change", AccessToken: "fresh-access", RefreshToken: "old-refresh",
		ClientID: "test-client", ClientSecret: "test-secret",
		ExpiresAt: time.Now().Add(time.Hour).Unix(), ProfileArn: "old-profile", Enabled: true,
	}
	if err := config.AddAccount(account); err != nil {
		t.Fatalf("config.AddAccount: %v", err)
	}
	p := accountpool.GetPool()
	p.ResetRuntimeState()
	p.Reload()
	h := &Handler{pool: p}
	if err := config.UpdateAccountProfileArn(account.ID, "new-profile"); err != nil {
		t.Fatalf("change profile ARN: %v", err)
	}

	var calls atomic.Int32
	stale := account
	if err := h.refreshAccountTokenWith(&stale, true, func(*config.Account) (string, string, int64, string, error) {
		calls.Add(1)
		return "forced-access", "forced-refresh", time.Now().Add(time.Hour).Unix(), "new-profile", nil
	}); err != nil {
		t.Fatalf("forced refresh: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("forced refresh calls = %d, want 1", calls.Load())
	}
	stored, ok := config.GetAccount(account.ID)
	if !ok || stored.AccessToken != "forced-access" || stored.RefreshToken != "forced-refresh" {
		t.Fatalf("forced token refresh was skipped after profile-only change: %#v", stored)
	}
}

func TestRefreshPendingCredentialsRecoverAfterReloadWithoutReplay(t *testing.T) {
	configPath := t.TempDir() + "/config.json"
	if err := config.Init(configPath); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	account := config.Account{
		ID: "durable-pending", AccessToken: "old-access", RefreshToken: "old-refresh",
		ClientID: "test-client", ClientSecret: "test-secret", ExpiresAt: 1, Enabled: true,
	}
	if err := config.AddAccount(account); err != nil {
		t.Fatalf("config.AddAccount: %v", err)
	}
	p := accountpool.GetPool()
	p.ResetRuntimeState()
	p.Reload()
	h := &Handler{pool: p}
	if err := config.BeginAccountTokenRefresh(account.ID, account.RefreshToken); err != nil {
		t.Fatalf("begin refresh: %v", err)
	}
	if err := config.StageAccountRefreshCredentials(account.ID, account.RefreshToken, "rotated-access", "rotated-refresh", time.Now().Add(time.Hour).Unix(), "rotated-profile"); err != nil {
		t.Fatalf("stage rotated credentials: %v", err)
	}

	blockedPath := t.TempDir() + "/not-a-config-file"
	if err := os.Mkdir(blockedPath, 0700); err != nil {
		t.Fatalf("create blocked path: %v", err)
	}
	if err := config.Init(blockedPath); err == nil {
		t.Fatal("config.Init on directory unexpectedly succeeded")
	}
	if err := config.UpdateAccountCredentialsForRefresh(account.ID, account.RefreshToken, "rotated-access", "rotated-refresh", time.Now().Add(time.Hour).Unix(), "rotated-profile"); err == nil {
		t.Fatal("final credential commit unexpectedly succeeded while config path was invalid")
	}
	if err := config.Init(configPath); err != nil {
		t.Fatalf("reload durable pending credentials: %v", err)
	}
	p.Reload()
	delete(pendingRefreshByAccount, account.ID) // only the on-disk stage survives restart

	var refreshCalls atomic.Int32
	stale := account
	if err := h.refreshAccountTokenWith(&stale, true, func(*config.Account) (string, string, int64, string, error) {
		refreshCalls.Add(1)
		return "unexpected-access", "unexpected-refresh", 1, "", nil
	}); err != nil {
		t.Fatalf("recover staged credentials without replaying grant: %v", err)
	}
	if refreshCalls.Load() != 0 {
		t.Fatalf("refresh grants after reload = %d, want 0", refreshCalls.Load())
	}
	if stale.PendingRefresh != nil || stale.RefreshTokenUncertain {
		t.Fatalf("caller snapshot retained pending refresh state after recovery: %#v", stale)
	}
	stored, ok := config.GetAccount(account.ID)
	if !ok || stored.AccessToken != "rotated-access" || stored.RefreshToken != "rotated-refresh" || stored.ProfileArn != "rotated-profile" || stored.RefreshTokenUncertain || stored.PendingRefresh != nil {
		t.Fatalf("durable pending credentials were not committed: %#v", stored)
	}
	if got := p.GetByID(account.ID); got == nil || got.AccessToken != "rotated-access" || got.RefreshToken != "rotated-refresh" || got.ProfileArn != "rotated-profile" || got.RefreshTokenUncertain || got.PendingRefresh != nil {
		t.Fatalf("pool was not synchronized during recovery: %#v", got)
	}
}

func TestBackgroundRefreshRenewsExpiredTokenBeforeRefreshingAccountInfo(t *testing.T) {
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	account := config.Account{
		ID: "background-refresh", AccessToken: "expired-access", RefreshToken: "old-refresh",
		ClientID: "test-client", ClientSecret: "test-secret", ExpiresAt: 1, Enabled: true,
	}
	if err := config.AddAccount(account); err != nil {
		t.Fatalf("config.AddAccount: %v", err)
	}
	p := accountpool.GetPool()
	p.ResetRuntimeState()
	p.Reload()
	h := &Handler{pool: p}

	var refreshCalls atomic.Int32
	var infoCalls atomic.Int32
	h.refreshAllAccountsWith(
		func(got *config.Account) (string, string, int64, string, error) {
			refreshCalls.Add(1)
			if got.RefreshToken != "old-refresh" {
				t.Fatalf("refresh used unexpected source token")
			}
			return "new-access", "new-refresh", time.Now().Add(time.Hour).Unix(), "new-profile", nil
		},
		func(got *config.Account) (*config.AccountInfo, error) {
			infoCalls.Add(1)
			if got.AccessToken != "new-access" {
				t.Fatalf("account info used stale access token")
			}
			return &config.AccountInfo{Email: "test@example.invalid", SubscriptionType: "PRO"}, nil
		},
	)

	if refreshCalls.Load() != 1 || infoCalls.Load() != 1 {
		t.Fatalf("refresh calls=%d info calls=%d, want 1 each", refreshCalls.Load(), infoCalls.Load())
	}
	stored, ok := config.GetAccount(account.ID)
	if !ok || stored.AccessToken != "new-access" || stored.RefreshToken != "new-refresh" || stored.SubscriptionType != "PRO" {
		t.Fatalf("background refresh did not persist refreshed account state: %#v", stored)
	}
	if got := p.GetByID(account.ID); got == nil || got.AccessToken != "new-access" || got.RefreshToken != "new-refresh" {
		t.Fatalf("background refresh did not update pool tokens: %#v", got)
	}
}
