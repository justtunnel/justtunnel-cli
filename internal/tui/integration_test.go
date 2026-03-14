package tui

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// --- Integration tests: multi-tunnel lifecycle via TunnelManager + Model ---

// TestMultiTunnelSpawn verifies that adding multiple tunnels via the manager
// makes them all appear in Tunnels() with correct ports, names, and initial state.
func TestMultiTunnelSpawn(t *testing.T) {
	t.Parallel()

	collector := newMsgCollector()
	mocks := make(map[int]*mockTunnel)
	mgr := NewTunnelManager(mockTunnelFactory(mocks), collector)

	err := mgr.Add(3000, "api", "")
	if err != nil {
		t.Fatalf("Add(3000) failed: %v", err)
	}
	err = mgr.Add(8080, "web", "")
	if err != nil {
		t.Fatalf("Add(8080) failed: %v", err)
	}

	tunnels := mgr.Tunnels()
	if len(tunnels) != 2 {
		t.Fatalf("expected 2 tunnels, got %d", len(tunnels))
	}

	// Verify first tunnel
	if tunnels[0].Port != 3000 {
		t.Errorf("tunnels[0].Port = %d, want 3000", tunnels[0].Port)
	}
	if tunnels[0].Name != "api" {
		t.Errorf("tunnels[0].Name = %q, want %q", tunnels[0].Name, "api")
	}
	if tunnels[0].State != StateConnecting {
		t.Errorf("tunnels[0].State = %v, want StateConnecting", tunnels[0].State)
	}

	// Verify second tunnel
	if tunnels[1].Port != 8080 {
		t.Errorf("tunnels[1].Port = %d, want 8080", tunnels[1].Port)
	}
	if tunnels[1].Name != "web" {
		t.Errorf("tunnels[1].Name = %q, want %q", tunnels[1].Name, "web")
	}
	if tunnels[1].State != StateConnecting {
		t.Errorf("tunnels[1].State = %v, want StateConnecting", tunnels[1].State)
	}

	// Verify mock runners were created
	if _, exists := mocks[3000]; !exists {
		t.Error("mock runner not created for port 3000")
	}
	if _, exists := mocks[8080]; !exists {
		t.Error("mock runner not created for port 8080")
	}
}

// TestMultiTunnelSpawnWithModelView verifies that spawning tunnels through the
// model/manager wiring results in correct display entries and renderable views.
func TestMultiTunnelSpawnWithModelView(t *testing.T) {
	t.Parallel()

	mocks := make(map[int]*mockTunnel)
	collector := newMsgCollector()
	mgr := NewTunnelManager(mockTunnelFactory(mocks), collector)
	model := NewModelWithManager(mgr, PlanInfo{Name: "pro", MaxTunnels: 5})

	// Add tunnels through the model command flow
	model = typeCommand(t, model, "/add 3000 --name api")
	model, _ = pressEnter(t, model)

	model = typeCommand(t, model, "/add 8080 --name web")
	model, _ = pressEnter(t, model)

	if len(model.tunnels) != 2 {
		t.Fatalf("expected 2 display entries, got %d", len(model.tunnels))
	}

	// Simulate both tunnels connecting
	managed3000 := mgr.getManagedTunnel(3000)
	managed3000.Callbacks.OnConnected("api-sub", "https://api-sub.justtunnel.dev", "localhost:3000")

	managed8080 := mgr.getManagedTunnel(8080)
	managed8080.Callbacks.OnConnected("web-sub", "https://web-sub.justtunnel.dev", "localhost:8080")

	// Process the TunnelConnectedMsg messages through the model
	for _, msg := range collector.Messages() {
		if connMsg, ok := msg.(TunnelConnectedMsg); ok {
			updatedModel, _ := model.Update(connMsg)
			model = updatedModel.(Model)
		}
	}

	// Verify display state was updated
	output := model.View()
	if !strings.Contains(output, "3000") {
		t.Error("list view missing port 3000")
	}
	if !strings.Contains(output, "8080") {
		t.Error("list view missing port 8080")
	}
	if !strings.Contains(output, "2/5") {
		t.Error("list view missing plan quota 2/5")
	}
}

// TestDuplicatePortRejection verifies that adding the same port twice produces
// an error without corrupting the tunnel list.
func TestDuplicatePortRejection(t *testing.T) {
	t.Parallel()

	mocks := make(map[int]*mockTunnel)
	collector := newMsgCollector()
	mgr := NewTunnelManager(mockTunnelFactory(mocks), collector)
	model := NewModelWithManager(mgr, PlanInfo{Name: "pro", MaxTunnels: 5})

	// Add port 3000
	model = typeCommand(t, model, "/add 3000")
	model, _ = pressEnter(t, model)

	if model.errorMessage != "" {
		t.Fatalf("unexpected error on first add: %s", model.errorMessage)
	}
	if len(model.tunnels) != 1 {
		t.Fatalf("expected 1 tunnel, got %d", len(model.tunnels))
	}

	// Try to add port 3000 again
	model = typeCommand(t, model, "/add 3000")
	model, _ = pressEnter(t, model)

	if model.errorMessage == "" {
		t.Fatal("expected error for duplicate port, got none")
	}
	if !strings.Contains(model.errorMessage, "already running") {
		t.Errorf("error message = %q, expected to contain 'already running'", model.errorMessage)
	}

	// Verify only 1 tunnel exists, not 2
	if len(model.tunnels) != 1 {
		t.Errorf("expected 1 tunnel after duplicate rejection, got %d", len(model.tunnels))
	}
	if mgr.Count() != 1 {
		t.Errorf("manager count = %d, want 1", mgr.Count())
	}
}

// TestReconnectionIndependence verifies that when one tunnel disconnects,
// the other tunnel remains connected and unaffected.
func TestReconnectionIndependence(t *testing.T) {
	t.Parallel()

	collector := newMsgCollector()
	mocks := make(map[int]*mockTunnel)
	mgr := NewTunnelManager(mockTunnelFactory(mocks), collector)

	// Add and connect two tunnels
	err := mgr.Add(3000, "api", "")
	if err != nil {
		t.Fatalf("Add(3000) failed: %v", err)
	}
	err = mgr.Add(8080, "web", "")
	if err != nil {
		t.Fatalf("Add(8080) failed: %v", err)
	}

	managedAPI := mgr.getManagedTunnel(3000)
	managedWeb := mgr.getManagedTunnel(8080)

	// Both connect successfully
	managedAPI.Callbacks.OnConnected("api-sub", "https://api-sub.justtunnel.dev", "localhost:3000")
	managedWeb.Callbacks.OnConnected("web-sub", "https://web-sub.justtunnel.dev", "localhost:8080")

	// Verify both connected
	if managedAPI.State != StateConnected {
		t.Fatalf("api tunnel state = %v, want StateConnected", managedAPI.State)
	}
	if managedWeb.State != StateConnected {
		t.Fatalf("web tunnel state = %v, want StateConnected", managedWeb.State)
	}

	// Disconnect the API tunnel
	managedAPI.Callbacks.OnDisconnected(time.Now())

	// API tunnel should be disconnected
	if managedAPI.State != StateDisconnected {
		t.Errorf("api tunnel state after disconnect = %v, want StateDisconnected", managedAPI.State)
	}

	// Web tunnel should STILL be connected
	if managedWeb.State != StateConnected {
		t.Errorf("web tunnel state after api disconnect = %v, want StateConnected (independent)", managedWeb.State)
	}

	// Verify both tunnels are still in the manager
	tunnels := mgr.Tunnels()
	if len(tunnels) != 2 {
		t.Errorf("expected 2 tunnels still in manager, got %d", len(tunnels))
	}
}

// TestGracefulShutdownMultipleTunnels verifies that Shutdown() stops all tunnels
// concurrently and clears the manager state.
func TestGracefulShutdownMultipleTunnels(t *testing.T) {
	t.Parallel()

	mocks := make(map[int]*mockTunnel)
	collector := newMsgCollector()
	mgr := NewTunnelManager(mockTunnelFactory(mocks), collector)

	// Add and connect two tunnels
	_ = mgr.Add(3000, "api", "")
	_ = mgr.Add(8080, "web", "")

	managedAPI := mgr.getManagedTunnel(3000)
	managedWeb := mgr.getManagedTunnel(8080)

	managedAPI.Callbacks.OnConnected("api-sub", "https://api-sub.justtunnel.dev", "localhost:3000")
	managedWeb.Callbacks.OnConnected("web-sub", "https://web-sub.justtunnel.dev", "localhost:8080")

	// Verify tunnels are running
	if mgr.Count() != 2 {
		t.Fatalf("expected 2 tunnels before shutdown, got %d", mgr.Count())
	}

	// Shutdown all
	mgr.Shutdown()

	// All tunnels should be cleared
	if mgr.Count() != 0 {
		t.Errorf("expected 0 tunnels after shutdown, got %d", mgr.Count())
	}

	tunnels := mgr.Tunnels()
	if len(tunnels) != 0 {
		t.Errorf("expected empty tunnel list after shutdown, got %d", len(tunnels))
	}
}

// TestSubdomainChangeOnReconnectResetsStats verifies that reconnecting with a
// different subdomain resets stats, while reconnecting with the same subdomain
// preserves them.
func TestSubdomainChangeOnReconnectResetsStats(t *testing.T) {
	t.Parallel()

	t.Run("different subdomain resets stats", func(t *testing.T) {
		t.Parallel()

		collector := newMsgCollector()
		mgr := NewTunnelManager(mockTunnelFactory(nil), collector)

		_ = mgr.Add(8080, "web", "")
		managed := mgr.getManagedTunnel(8080)

		// Connect and record some requests
		managed.Callbacks.OnConnected("old-sub", "https://old-sub.justtunnel.dev", "localhost:8080")
		managed.Callbacks.OnRequest("GET", "/api/users", 200, 25*time.Millisecond)
		managed.Callbacks.OnRequest("POST", "/api/data", 201, 50*time.Millisecond)

		if managed.Stats.TotalCount() != 2 {
			t.Fatalf("expected 2 requests recorded, got %d", managed.Stats.TotalCount())
		}

		// Reconnect with different subdomain
		managed.Callbacks.OnReconnected("new-sub", "old-sub", "https://new-sub.justtunnel.dev", true)

		// Stats should be reset
		if managed.Stats.TotalCount() != 0 {
			t.Errorf("expected 0 requests after subdomain change, got %d", managed.Stats.TotalCount())
		}

		// Subdomain and URL should be updated
		if managed.Subdomain != "new-sub" {
			t.Errorf("subdomain = %q, want %q", managed.Subdomain, "new-sub")
		}
		if managed.PublicURL != "https://new-sub.justtunnel.dev" {
			t.Errorf("publicURL = %q, want %q", managed.PublicURL, "https://new-sub.justtunnel.dev")
		}
		if managed.LastSubdomain != "old-sub" {
			t.Errorf("lastSubdomain = %q, want %q", managed.LastSubdomain, "old-sub")
		}

		// Verify TunnelReconnectedMsg was sent
		msgs := collector.Messages()
		foundReconnectMsg := false
		for _, msg := range msgs {
			if reconMsg, ok := msg.(TunnelReconnectedMsg); ok {
				if reconMsg.Port == 8080 && reconMsg.SubdomainChanged && reconMsg.NewSubdomain == "new-sub" {
					foundReconnectMsg = true
					break
				}
			}
		}
		if !foundReconnectMsg {
			t.Error("expected TunnelReconnectedMsg with SubdomainChanged=true and NewSubdomain='new-sub'")
		}
	})

	t.Run("same subdomain preserves stats", func(t *testing.T) {
		t.Parallel()

		collector := newMsgCollector()
		mgr := NewTunnelManager(mockTunnelFactory(nil), collector)

		_ = mgr.Add(8080, "web", "")
		managed := mgr.getManagedTunnel(8080)

		// Connect and record requests
		managed.Callbacks.OnConnected("my-sub", "https://my-sub.justtunnel.dev", "localhost:8080")
		managed.Callbacks.OnRequest("GET", "/health", 200, 5*time.Millisecond)
		managed.Callbacks.OnRequest("GET", "/status", 200, 8*time.Millisecond)
		managed.Callbacks.OnRequest("POST", "/webhook", 200, 12*time.Millisecond)

		if managed.Stats.TotalCount() != 3 {
			t.Fatalf("expected 3 requests recorded, got %d", managed.Stats.TotalCount())
		}

		// Reconnect with same subdomain
		managed.Callbacks.OnReconnected("my-sub", "my-sub", "https://my-sub.justtunnel.dev", false)

		// Stats should be preserved
		if managed.Stats.TotalCount() != 3 {
			t.Errorf("expected 3 requests preserved after same-subdomain reconnect, got %d", managed.Stats.TotalCount())
		}
	})
}

// TestConcurrentTunnelOperations verifies that concurrent Add/Remove/Tunnels
// operations on the manager do not race. This test is meaningful under -race.
func TestConcurrentTunnelOperations(t *testing.T) {
	t.Parallel()

	collector := newMsgCollector()
	mgr := NewTunnelManager(mockTunnelFactory(nil), collector)

	var wg sync.WaitGroup
	const concurrentOps = 20

	// Concurrently add tunnels on different ports
	for portOffset := range concurrentOps {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			_ = mgr.Add(port, "svc", "")
		}(10000 + portOffset)
	}
	wg.Wait()

	if mgr.Count() != concurrentOps {
		t.Errorf("expected %d tunnels, got %d", concurrentOps, mgr.Count())
	}

	// Concurrently read tunnels while removing some
	var readWg sync.WaitGroup
	for idx := range concurrentOps {
		readWg.Add(1)
		go func(port int) {
			defer readWg.Done()
			// Interleave reads and removals
			_ = mgr.Tunnels()
			if port%2 == 0 {
				_ = mgr.RemoveByPort(port)
			}
		}(10000 + idx)
	}
	readWg.Wait()

	// After removing even-numbered ports, half should remain
	expectedRemaining := concurrentOps / 2
	if mgr.Count() != expectedRemaining {
		t.Errorf("expected %d tunnels after concurrent removal, got %d", expectedRemaining, mgr.Count())
	}
}

// TestMultiTunnelCallbackIsolation verifies that callbacks for one tunnel
// do not affect another tunnel's state or stats.
func TestMultiTunnelCallbackIsolation(t *testing.T) {
	t.Parallel()

	collector := newMsgCollector()
	mgr := NewTunnelManager(mockTunnelFactory(nil), collector)

	_ = mgr.Add(3000, "api", "")
	_ = mgr.Add(8080, "web", "")

	managedAPI := mgr.getManagedTunnel(3000)
	managedWeb := mgr.getManagedTunnel(8080)

	// Connect both
	managedAPI.Callbacks.OnConnected("api-sub", "https://api-sub.justtunnel.dev", "localhost:3000")
	managedWeb.Callbacks.OnConnected("web-sub", "https://web-sub.justtunnel.dev", "localhost:8080")

	// Record requests only on the API tunnel
	managedAPI.Callbacks.OnRequest("GET", "/api/users", 200, 10*time.Millisecond)
	managedAPI.Callbacks.OnRequest("POST", "/api/items", 201, 20*time.Millisecond)
	managedAPI.Callbacks.OnRequest("DELETE", "/api/items/1", 204, 15*time.Millisecond)

	// API should have 3 requests
	if managedAPI.Stats.TotalCount() != 3 {
		t.Errorf("api tunnel requests = %d, want 3", managedAPI.Stats.TotalCount())
	}

	// Web should have 0 requests (no cross-contamination)
	if managedWeb.Stats.TotalCount() != 0 {
		t.Errorf("web tunnel requests = %d, want 0 (no cross-contamination)", managedWeb.Stats.TotalCount())
	}

	// Verify subdomain isolation
	if managedAPI.Subdomain != "api-sub" {
		t.Errorf("api subdomain = %q, want %q", managedAPI.Subdomain, "api-sub")
	}
	if managedWeb.Subdomain != "web-sub" {
		t.Errorf("web subdomain = %q, want %q", managedWeb.Subdomain, "web-sub")
	}
}

// TestReconnectingStateDoesNotAffectOtherTunnels verifies that a reconnecting
// tunnel does not change the state of sibling tunnels.
func TestReconnectingStateDoesNotAffectOtherTunnels(t *testing.T) {
	t.Parallel()

	collector := newMsgCollector()
	mgr := NewTunnelManager(mockTunnelFactory(nil), collector)

	_ = mgr.Add(3000, "api", "")
	_ = mgr.Add(8080, "web", "")

	managedAPI := mgr.getManagedTunnel(3000)
	managedWeb := mgr.getManagedTunnel(8080)

	// Both connect
	managedAPI.Callbacks.OnConnected("api-sub", "https://api-sub.justtunnel.dev", "localhost:3000")
	managedWeb.Callbacks.OnConnected("web-sub", "https://web-sub.justtunnel.dev", "localhost:8080")

	// API tunnel starts reconnecting
	managedAPI.Callbacks.OnDisconnected(time.Now())
	managedAPI.Callbacks.OnReconnecting(1, 2*time.Second)

	// API should be reconnecting
	if managedAPI.State != StateReconnecting {
		t.Errorf("api state = %v, want StateReconnecting", managedAPI.State)
	}

	// Web should still be connected
	if managedWeb.State != StateConnected {
		t.Errorf("web state = %v, want StateConnected", managedWeb.State)
	}

	// API reconnects successfully with same subdomain
	managedAPI.Callbacks.OnReconnected("api-sub", "api-sub", "https://api-sub.justtunnel.dev", false)

	// Both should now be connected
	if managedAPI.State != StateConnected {
		t.Errorf("api state after reconnect = %v, want StateConnected", managedAPI.State)
	}
	if managedWeb.State != StateConnected {
		t.Errorf("web state after api reconnect = %v, want StateConnected", managedWeb.State)
	}
}

// TestFullLifecycleIntegration exercises the complete lifecycle: add tunnels,
// connect them, send requests, disconnect one, reconnect it, then shut down.
func TestFullLifecycleIntegration(t *testing.T) {
	t.Parallel()

	collector := newMsgCollector()
	mocks := make(map[int]*mockTunnel)
	mgr := NewTunnelManager(mockTunnelFactory(mocks), collector)
	model := NewModelWithManager(mgr, PlanInfo{Name: "pro", MaxTunnels: 5})

	// Phase 1: Add tunnels
	model = typeCommand(t, model, "/add 3000 --name api")
	model, _ = pressEnter(t, model)
	model = typeCommand(t, model, "/add 8080 --name web")
	model, _ = pressEnter(t, model)

	if len(model.tunnels) != 2 {
		t.Fatalf("expected 2 tunnels, got %d", len(model.tunnels))
	}

	// Phase 2: Connect both tunnels
	managed3000 := mgr.getManagedTunnel(3000)
	managed8080 := mgr.getManagedTunnel(8080)

	managed3000.Callbacks.OnConnected("api-sub", "https://api-sub.justtunnel.dev", "localhost:3000")
	managed8080.Callbacks.OnConnected("web-sub", "https://web-sub.justtunnel.dev", "localhost:8080")

	// Process connected messages
	for _, msg := range collector.Messages() {
		if connMsg, ok := msg.(TunnelConnectedMsg); ok {
			updatedModel, _ := model.Update(connMsg)
			model = updatedModel.(Model)
		}
	}

	// Both display entries should be connected
	for idx, entry := range model.tunnels {
		if entry.State != StateConnected {
			t.Errorf("tunnels[%d].State = %v, want StateConnected", idx, entry.State)
		}
	}

	// Phase 3: Send requests to API tunnel
	managed3000.Callbacks.OnRequest("GET", "/users", 200, 10*time.Millisecond)
	managed3000.Callbacks.OnRequest("POST", "/users", 201, 25*time.Millisecond)

	if managed3000.Stats.TotalCount() != 2 {
		t.Errorf("api requests = %d, want 2", managed3000.Stats.TotalCount())
	}

	// Phase 4: Disconnect API tunnel
	managed3000.Callbacks.OnDisconnected(time.Now())
	if managed3000.State != StateDisconnected {
		t.Errorf("api state after disconnect = %v, want StateDisconnected", managed3000.State)
	}

	// Web tunnel should be unaffected
	if managed8080.State != StateConnected {
		t.Errorf("web state = %v, want StateConnected (unaffected)", managed8080.State)
	}

	// Phase 5: Reconnect API tunnel with new subdomain
	managed3000.Callbacks.OnReconnecting(1, time.Second)
	managed3000.Callbacks.OnReconnected("api-new", "api-sub", "https://api-new.justtunnel.dev", true)

	if managed3000.State != StateConnected {
		t.Errorf("api state after reconnect = %v, want StateConnected", managed3000.State)
	}

	// Stats should be reset due to subdomain change
	if managed3000.Stats.TotalCount() != 0 {
		t.Errorf("api requests after subdomain change = %d, want 0", managed3000.Stats.TotalCount())
	}

	// Phase 6: Shutdown
	mgr.Shutdown()

	if mgr.Count() != 0 {
		t.Errorf("tunnels after shutdown = %d, want 0", mgr.Count())
	}
}
