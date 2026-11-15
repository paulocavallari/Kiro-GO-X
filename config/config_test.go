package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestUpdateAccountTokenPersistenceFailureDoesNotMutateMemory(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := Init(configPath); err != nil {
		t.Fatalf("init config: %v", err)
	}
	account := Account{ID: "account-1", AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: 10}
	if err := AddAccount(account); err != nil {
		t.Fatalf("add account: %v", err)
	}

	blockedPath := filepath.Join(t.TempDir(), "target-directory")
	if err := os.Mkdir(blockedPath, 0700); err != nil {
		t.Fatalf("create blocked target: %v", err)
	}
	originalPath := cfgPath
	t.Cleanup(func() { cfgPath = originalPath })
	cfgPath = blockedPath

	if err := UpdateAccountToken(account.ID, "new-access", "new-refresh", 20); err == nil {
		t.Fatal("expected persistence to fail when target is a directory")
	}
	stored := GetAccounts()
	if len(stored) != 1 {
		t.Fatal("account disappeared from in-memory config")
	}
	got := stored[0]
	if got.AccessToken != "old-access" || got.RefreshToken != "old-refresh" || got.ExpiresAt != 10 {
		t.Fatalf("failed persistence mutated in-memory credentials: %#v", got)
	}
}

func TestUpdateSettingsPatchPreservesOmittedAPIKeyFields(t *testing.T) {
	if err := Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("init config: %v", err)
	}
	if err := UpdateSettings("proxy-api-key", true, "admin-password"); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	if err := UpdateSettingsPatch(nil, nil, "new-admin-password"); err != nil {
		t.Fatalf("patch settings: %v", err)
	}

	if got := GetApiKey(); got != "proxy-api-key" {
		t.Fatalf("expected API key to be preserved, got %q", got)
	}
	if !IsApiKeyRequired() {
		t.Fatalf("expected requireApiKey to stay enabled")
	}
	if got := GetPassword(); got != "new-admin-password" {
		t.Fatalf("expected password to update, got %q", got)
	}
}

func TestUpdateSettingsPatchCanExplicitlyDisableAPIKey(t *testing.T) {
	if err := Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("init config: %v", err)
	}
	if err := UpdateSettings("proxy-api-key", true, "admin-password"); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	emptyKey := ""
	requireAPIKey := false
	if err := UpdateSettingsPatch(&emptyKey, &requireAPIKey, ""); err != nil {
		t.Fatalf("patch settings: %v", err)
	}

	if got := GetApiKey(); got != "" {
		t.Fatalf("expected API key to be cleared, got %q", got)
	}
	if IsApiKeyRequired() {
		t.Fatalf("expected requireApiKey to be disabled")
	}
	if got := GetPassword(); got != "admin-password" {
		t.Fatalf("expected password to be preserved, got %q", got)
	}
}

func TestUpdateAccountTokenPreservesExistingFileModeAndUnrelatedFields(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not preserved on Windows")
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := Init(configPath); err != nil {
		t.Fatalf("init config: %v", err)
	}
	account := Account{
		ID: "account-1", AccessToken: "old-access", RefreshToken: "old-refresh",
		ProfileArn: "arn:existing", Email: "owner@example.test", Enabled: true,
	}
	if err := AddAccount(account); err != nil {
		t.Fatalf("add account: %v", err)
	}
	if err := UpdateSettings("preserved-api-key", true, "preserved-password"); err != nil {
		t.Fatalf("seed unrelated config fields: %v", err)
	}
	if err := os.Chmod(configPath, 0640); err != nil {
		t.Fatalf("set shared config mode: %v", err)
	}

	if err := UpdateAccountToken(account.ID, "new-access", "new-refresh", 12345); err != nil {
		t.Fatalf("update credentials: %v", err)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0640 {
		t.Fatalf("config mode = %04o, want 0640", got)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	var persisted Config
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("decode persisted config: %v", err)
	}
	if persisted.ApiKey != "preserved-api-key" || persisted.Password != "preserved-password" {
		t.Fatalf("unrelated global fields changed: apiKey=%q password=%q", persisted.ApiKey, persisted.Password)
	}
	if len(persisted.Accounts) != 1 || persisted.Accounts[0].AccessToken != "new-access" || persisted.Accounts[0].RefreshToken != "new-refresh" || persisted.Accounts[0].ProfileArn != "arn:existing" || persisted.Accounts[0].Email != account.Email {
		t.Fatalf("persisted account fields were not preserved: %#v", persisted.Accounts)
	}
}

func TestStaleRefreshResultCannotOverwriteReauthenticatedCredentials(t *testing.T) {
	if err := Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("init config: %v", err)
	}
	account := Account{ID: "account-cas", AccessToken: "old-access", RefreshToken: "old-refresh", Enabled: true}
	if err := AddAccount(account); err != nil {
		t.Fatalf("add account: %v", err)
	}
	if err := BeginAccountTokenRefresh(account.ID, account.RefreshToken); err != nil {
		t.Fatalf("mark refresh in progress: %v", err)
	}
	if marked, ok := GetAccount(account.ID); !ok || !marked.RefreshTokenUncertain {
		t.Fatalf("refresh uncertainty marker was not saved: %#v", marked)
	}

	if err := UpdateAccountToken(account.ID, "reauth-access", "reauth-refresh", 1); err != nil {
		t.Fatalf("install reauthenticated credentials: %v", err)
	}
	if err := UpdateAccountCredentialsForRefresh(account.ID, "old-refresh", "stale-access", "stale-refresh", 2, ""); err != ErrAccountCredentialsChanged {
		t.Fatalf("stale refresh result error = %v, want ErrAccountCredentialsChanged", err)
	}
	current, ok := GetAccount(account.ID)
	if !ok || current.AccessToken != "reauth-access" || current.RefreshToken != "reauth-refresh" || current.RefreshTokenUncertain {
		t.Fatalf("reauthenticated credentials were overwritten or left blocked: %#v", current)
	}
}

func TestMetadataUpdateCannotOverwriteRotatedCredentialsFromStaleSnapshot(t *testing.T) {
	if err := Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("init config: %v", err)
	}
	account := Account{ID: "metadata-cas", AccessToken: "old-access", RefreshToken: "old-refresh", Enabled: true, Nickname: "old-name"}
	if err := AddAccount(account); err != nil {
		t.Fatalf("add account: %v", err)
	}
	stale, ok := GetAccount(account.ID)
	if !ok {
		t.Fatal("account missing before refresh")
	}
	stale.Nickname = "operator-name"
	if err := UpdateAccountToken(account.ID, "rotated-access", "rotated-refresh", 1234); err != nil {
		t.Fatalf("commit rotated credentials: %v", err)
	}
	if err := UpdateAccountProfileArn(account.ID, "rotated-profile"); err != nil {
		t.Fatalf("commit rotated profile ARN: %v", err)
	}
	if err := UpdateAccount(account.ID, stale); err != nil {
		t.Fatalf("apply metadata-only update: %v", err)
	}
	current, ok := GetAccount(account.ID)
	if !ok || current.Nickname != "operator-name" || current.AccessToken != "rotated-access" || current.RefreshToken != "rotated-refresh" || current.ExpiresAt != 1234 || current.ProfileArn != "rotated-profile" {
		t.Fatalf("stale metadata snapshot overwrote rotated credentials: %#v", current)
	}
}

func TestRefreshCommitRejectsSameTokenReauthAfterStage(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := Init(configPath); err != nil {
		t.Fatalf("init config: %v", err)
	}
	account := Account{ID: "refresh-commit-cas", AccessToken: "old-access", RefreshToken: "old-refresh", Enabled: true}
	if err := AddAccount(account); err != nil {
		t.Fatalf("add account: %v", err)
	}
	if err := BeginAccountTokenRefresh(account.ID, account.RefreshToken); err != nil {
		t.Fatalf("begin refresh: %v", err)
	}
	if err := StageAccountRefreshCredentials(account.ID, account.RefreshToken, "rotated-access", "rotated-refresh", 1234, "rotated-profile"); err != nil {
		t.Fatalf("stage refresh result: %v", err)
	}
	if err := UpdateAccountToken(account.ID, "reauth-access", "old-refresh", 5678); err != nil {
		t.Fatalf("install reauthenticated token with same refresh token: %v", err)
	}
	if err := UpdateAccountCredentialsForRefresh(account.ID, "old-refresh", "rotated-access", "rotated-refresh", 1234, "rotated-profile"); err != ErrAccountCredentialsChanged {
		t.Fatalf("stale final commit error = %v, want ErrAccountCredentialsChanged", err)
	}
	current, ok := GetAccount(account.ID)
	if !ok || current.AccessToken != "reauth-access" || current.RefreshToken != "old-refresh" || current.ExpiresAt != 5678 || current.RefreshTokenUncertain || current.PendingRefresh != nil {
		t.Fatalf("stale final commit overwrote reauthenticated credentials: %#v", current)
	}
}

func TestPendingRefreshCredentialsSurviveReloadAndCommit(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := Init(configPath); err != nil {
		t.Fatalf("init config: %v", err)
	}
	account := Account{ID: "pending-reload", AccessToken: "old-access", RefreshToken: "old-refresh", Enabled: true}
	if err := AddAccount(account); err != nil {
		t.Fatalf("add account: %v", err)
	}
	if err := BeginAccountTokenRefresh(account.ID, account.RefreshToken); err != nil {
		t.Fatalf("begin refresh: %v", err)
	}
	if err := StageAccountRefreshCredentials(account.ID, account.RefreshToken, "new-access", "new-refresh", 456, "new-profile"); err != nil {
		t.Fatalf("stage refreshed credentials: %v", err)
	}
	selection, ok := GetAccount(account.ID)
	if !ok || selection.PendingRefresh == nil {
		t.Fatalf("pending refresh snapshot is missing: %#v", selection)
	}
	selection.PendingRefresh.AccessToken = "mutated-snapshot"
	if pending, ok := GetPendingAccountRefresh(account.ID, account.RefreshToken); !ok || pending.AccessToken != "new-access" {
		t.Fatalf("caller mutated config through pending-refresh snapshot: %#v", pending)
	}

	if err := Init(configPath); err != nil {
		t.Fatalf("reload staged config: %v", err)
	}
	pending, ok := GetPendingAccountRefresh(account.ID, account.RefreshToken)
	if !ok || pending.AccessToken != "new-access" || pending.RefreshToken != "new-refresh" || pending.ProfileArn != "new-profile" {
		t.Fatalf("pending refresh did not survive reload: %#v, ok=%v", pending, ok)
	}
	if err := UpdateAccountCredentialsForRefresh(account.ID, account.RefreshToken, pending.AccessToken, pending.RefreshToken, pending.ExpiresAt, pending.ProfileArn); err != nil {
		t.Fatalf("commit pending refresh: %v", err)
	}
	current, ok := GetAccount(account.ID)
	if !ok || current.AccessToken != "new-access" || current.RefreshToken != "new-refresh" || current.ProfileArn != "new-profile" || current.RefreshTokenUncertain || current.PendingRefresh != nil {
		t.Fatalf("pending credentials were not committed and cleared: %#v", current)
	}
}
