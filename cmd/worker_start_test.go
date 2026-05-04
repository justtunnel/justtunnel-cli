package cmd

import (
	"strings"
	"testing"
)

// TestWorkerStartRequiresLocalConfig verifies the start command refuses to
// dial when no `worker create` was run for the given name. Without this,
// `worker start <typo>` would silently invent a worker_id and burn
// reconnect attempts forever.
func TestWorkerStartRequiresLocalConfig(t *testing.T) {
	resetWorkerState(t, teamCfg("http://unused.invalid"))

	out, err := runCmd(t, "worker", "start", "nonexistent")
	if err == nil {
		t.Fatalf("expected error for missing worker config; got success: %s", out)
	}
	if !strings.Contains(err.Error(), "worker create") {
		t.Errorf("error message should suggest `worker create`; got: %v", err)
	}
}

// TestWorkerStartRequiresAuth ensures the runner refuses to dial when the
// user is not signed in. This protects against burning reconnect loops on
// guaranteed-401s.
func TestWorkerStartRequiresAuth(t *testing.T) {
	cfg := teamCfg("http://unused.invalid")
	cfg.AuthToken = ""
	resetWorkerState(t, cfg)

	_, err := runCmd(t, "worker", "start", "anyname")
	if err == nil {
		t.Fatal("expected auth error; got success")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "sign") {
		t.Errorf("expected auth error mentioning sign-in; got: %v", err)
	}
}

// TestWorkerStartRegistered verifies the subcommand is wired into the
// `worker` parent command. Catches the "forgot to AddCommand" foot-gun.
func TestWorkerStartRegistered(t *testing.T) {
	for _, child := range workerCmd.Commands() {
		if child.Name() == "start" {
			return
		}
	}
	t.Fatal("worker start subcommand is not registered under workerCmd")
}
