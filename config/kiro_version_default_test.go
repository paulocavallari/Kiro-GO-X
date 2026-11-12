package config

import "testing"

func TestDefaultKiroVersionSupported(t *testing.T) {
	cfgLock.Lock()
	previous := cfg
	cfg = nil
	cfgLock.Unlock()
	t.Cleanup(func() {
		cfgLock.Lock()
		cfg = previous
		cfgLock.Unlock()
	})
	if got := GetKiroClientConfig().KiroVersion; got != "1.4.121" {
		t.Fatalf("default Kiro version = %q; want 1.4.121", got)
	}
}
