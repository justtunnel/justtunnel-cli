package tui

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// mockTunnel simulates a tunnel.Tunnel for testing the manager layer.
// It records lifecycle calls and can trigger callbacks without real WebSocket connections.
type mockTunnel struct {
	port       int
	running    bool
	shutdownCh chan struct{}
	runErr     error
	mu         sync.Mutex
}

func newMockTunnel(port int) *mockTunnel {
	return &mockTunnel{
		port:       port,
		shutdownCh: make(chan struct{}),
	}
}

func (m *mockTunnel) Run(ctx context.Context) error {
	m.mu.Lock()
	m.running = true
	m.mu.Unlock()

	if m.runErr != nil {
		return m.runErr
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.shutdownCh:
		return nil
	}
}

func (m *mockTunnel) Shutdown(timeout time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		m.running = false
		select {
		case <-m.shutdownCh:
		default:
			close(m.shutdownCh)
		}
	}
}

// msgCollector is a mock tea.Program that collects sent messages for assertion.
type msgCollector struct {
	mu       sync.Mutex
	messages []tea.Msg
}

func newMsgCollector() *msgCollector {
	return &msgCollector{
		messages: make([]tea.Msg, 0),
	}
}

func (mc *msgCollector) Send(msg tea.Msg) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.messages = append(mc.messages, msg)
}

func (mc *msgCollector) Messages() []tea.Msg {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	result := make([]tea.Msg, len(mc.messages))
	copy(result, mc.messages)
	return result
}

// mockTunnelFactory creates a factory that produces mockTunnels and records them.
func mockTunnelFactory(mocks map[int]*mockTunnel) TunnelFactory {
	return func(port int, name string, subdomain string, password string, callbacks TunnelCallbacks) TunnelRunner {
		mock := newMockTunnel(port)
		if mocks != nil {
			mocks[port] = mock
		}
		return mock
	}
}

// mockTunnelFactoryWithError creates a factory that produces mockTunnels
// pre-configured to return the given error from Run().
func mockTunnelFactoryWithError(runErr error, mocks map[int]*mockTunnel) TunnelFactory {
	return func(port int, name string, subdomain string, password string, callbacks TunnelCallbacks) TunnelRunner {
		mock := newMockTunnel(port)
		mock.runErr = runErr
		if mocks != nil {
			mocks[port] = mock
		}
		return mock
	}
}

func TestManagerAddTunnel(t *testing.T) {
	t.Run("add tunnel appears in list with correct port and state", func(t *testing.T) {
		mgr := NewTunnelManager(mockTunnelFactory(nil), nil)

		err := mgr.Add(8080, "web", "", "")
		if err != nil {
			t.Fatalf("Add(8080) returned unexpected error: %v", err)
		}

		tunnels := mgr.Tunnels()
		if len(tunnels) != 1 {
			t.Fatalf("expected 1 tunnel, got %d", len(tunnels))
		}

		if tunnels[0].Port != 8080 {
			t.Errorf("expected port 8080, got %d", tunnels[0].Port)
		}
		if tunnels[0].Name != "web" {
			t.Errorf("expected name %q, got %q", "web", tunnels[0].Name)
		}
		if tunnels[0].State != StateConnecting {
			t.Errorf("expected state %v, got %v", StateConnecting, tunnels[0].State)
		}
	})

	t.Run("add multiple tunnels in insertion order", func(t *testing.T) {
		mgr := NewTunnelManager(mockTunnelFactory(nil), nil)

		_ = mgr.Add(3000, "api", "", "")
		_ = mgr.Add(8080, "web", "", "")
		_ = mgr.Add(5432, "db", "", "")

		tunnels := mgr.Tunnels()
		if len(tunnels) != 3 {
			t.Fatalf("expected 3 tunnels, got %d", len(tunnels))
		}

		expectedPorts := []int{3000, 8080, 5432}
		for idx, want := range expectedPorts {
			if tunnels[idx].Port != want {
				t.Errorf("tunnels[%d].Port = %d, want %d", idx, tunnels[idx].Port, want)
			}
		}
	})
}

func TestManagerAddDuplicatePort(t *testing.T) {
	mgr := NewTunnelManager(mockTunnelFactory(nil), nil)

	err := mgr.Add(8080, "web", "", "")
	if err != nil {
		t.Fatalf("first Add(8080) failed: %v", err)
	}

	err = mgr.Add(8080, "web2", "", "")
	if err == nil {
		t.Fatal("expected error for duplicate port, got nil")
	}

	// Verify no state change — still only one tunnel
	tunnels := mgr.Tunnels()
	if len(tunnels) != 1 {
		t.Errorf("expected 1 tunnel after duplicate add, got %d", len(tunnels))
	}
}

func TestManagerAddInvalidPort(t *testing.T) {
	tests := []struct {
		name string
		port int
	}{
		{"zero port", 0},
		{"negative port", -1},
		{"port too high", 99999},
		{"port above 65535", 65536},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := NewTunnelManager(mockTunnelFactory(nil), nil)
			err := mgr.Add(tt.port, "", "", "")
			if err == nil {
				t.Errorf("expected error for port %d, got nil", tt.port)
			}
		})
	}
}

func TestManagerRemoveByIndex(t *testing.T) {
	mocks := make(map[int]*mockTunnel)
	mgr := NewTunnelManager(mockTunnelFactory(mocks), nil)

	_ = mgr.Add(3000, "api", "", "")
	_ = mgr.Add(8080, "web", "", "")
	_ = mgr.Add(5432, "db", "", "")

	// Remove index 2 (1-based) = port 8080
	err := mgr.RemoveByIndex(2)
	if err != nil {
		t.Fatalf("RemoveByIndex(2) failed: %v", err)
	}

	tunnels := mgr.Tunnels()
	if len(tunnels) != 2 {
		t.Fatalf("expected 2 tunnels after removal, got %d", len(tunnels))
	}

	// Verify remaining tunnels maintain insertion order
	if tunnels[0].Port != 3000 {
		t.Errorf("tunnels[0].Port = %d, want 3000", tunnels[0].Port)
	}
	if tunnels[1].Port != 5432 {
		t.Errorf("tunnels[1].Port = %d, want 5432", tunnels[1].Port)
	}
}

func TestManagerRemoveByPort(t *testing.T) {
	mocks := make(map[int]*mockTunnel)
	mgr := NewTunnelManager(mockTunnelFactory(mocks), nil)

	_ = mgr.Add(3000, "api", "", "")
	_ = mgr.Add(8080, "web", "", "")

	err := mgr.RemoveByPort(3000)
	if err != nil {
		t.Fatalf("RemoveByPort(3000) failed: %v", err)
	}

	tunnels := mgr.Tunnels()
	if len(tunnels) != 1 {
		t.Fatalf("expected 1 tunnel after removal, got %d", len(tunnels))
	}
	if tunnels[0].Port != 8080 {
		t.Errorf("expected remaining tunnel on port 8080, got %d", tunnels[0].Port)
	}
}

func TestManagerRemoveNonExistentIndex(t *testing.T) {
	mgr := NewTunnelManager(mockTunnelFactory(nil), nil)
	_ = mgr.Add(8080, "web", "", "")

	err := mgr.RemoveByIndex(5)
	if err == nil {
		t.Fatal("expected error for non-existent index, got nil")
	}

	expectedMsg := "No tunnel at index 5. Use /list to see active tunnels."
	if err.Error() != expectedMsg {
		t.Errorf("error message = %q, want %q", err.Error(), expectedMsg)
	}
}

func TestManagerRemoveNonExistentPort(t *testing.T) {
	mgr := NewTunnelManager(mockTunnelFactory(nil), nil)
	_ = mgr.Add(8080, "web", "", "")

	err := mgr.RemoveByPort(9999)
	if err == nil {
		t.Fatal("expected error for non-existent port, got nil")
	}
}

func TestManagerShutdownAll(t *testing.T) {
	mocks := make(map[int]*mockTunnel)
	mgr := NewTunnelManager(mockTunnelFactory(mocks), nil)

	_ = mgr.Add(3000, "api", "", "")
	_ = mgr.Add(8080, "web", "", "")

	mgr.Shutdown()

	tunnels := mgr.Tunnels()
	if len(tunnels) != 0 {
		t.Errorf("expected 0 tunnels after shutdown, got %d", len(tunnels))
	}
}

func TestManagerCountAfterRemoveAll(t *testing.T) {
	mocks := make(map[int]*mockTunnel)
	mgr := NewTunnelManager(mockTunnelFactory(mocks), nil)

	_ = mgr.Add(8080, "web", "", "")

	err := mgr.RemoveByPort(8080)
	if err != nil {
		t.Fatalf("RemoveByPort(8080) failed: %v", err)
	}

	// Count() reaches 0 — session stays active, no auto-quit signal
	if mgr.Count() != 0 {
		t.Errorf("expected count 0, got %d", mgr.Count())
	}
}

func TestManagerTunnelsStableInsertionOrder(t *testing.T) {
	mgr := NewTunnelManager(mockTunnelFactory(nil), nil)

	ports := []int{9090, 3000, 8080, 4000, 5000}
	for _, port := range ports {
		_ = mgr.Add(port, fmt.Sprintf("svc-%d", port), "", "")
	}

	tunnels := mgr.Tunnels()
	for idx, want := range ports {
		if tunnels[idx].Port != want {
			t.Errorf("tunnels[%d].Port = %d, want %d", idx, tunnels[idx].Port, want)
		}
	}

	// Remove middle tunnel and verify order is preserved
	_ = mgr.RemoveByPort(8080)

	tunnels = mgr.Tunnels()
	expectedAfterRemove := []int{9090, 3000, 4000, 5000}
	for idx, want := range expectedAfterRemove {
		if tunnels[idx].Port != want {
			t.Errorf("after remove: tunnels[%d].Port = %d, want %d", idx, tunnels[idx].Port, want)
		}
	}
}

func TestManagerCallbackBridge(t *testing.T) {
	t.Run("connected callback sends TunnelConnectedMsg", func(t *testing.T) {
		collector := newMsgCollector()
		mgr := NewTunnelManager(mockTunnelFactory(nil), collector)

		_ = mgr.Add(8080, "web", "", "")

		// Simulate the connected callback
		managed := mgr.getManagedTunnel(8080)
		if managed == nil {
			t.Fatal("expected managed tunnel for port 8080")
		}
		managed.Callbacks.OnConnected("test-sub", "https://test-sub.justtunnel.dev", "localhost:8080", false)

		msgs := collector.Messages()
		if len(msgs) == 0 {
			t.Fatal("expected at least one message from connected callback")
		}

		connMsg, ok := msgs[len(msgs)-1].(TunnelConnectedMsg)
		if !ok {
			t.Fatalf("expected TunnelConnectedMsg, got %T", msgs[len(msgs)-1])
		}
		if connMsg.Port != 8080 {
			t.Errorf("Port = %d, want 8080", connMsg.Port)
		}
		if connMsg.Subdomain != "test-sub" {
			t.Errorf("Subdomain = %q, want %q", connMsg.Subdomain, "test-sub")
		}
	})

	t.Run("disconnected callback sends TunnelDisconnectedMsg", func(t *testing.T) {
		collector := newMsgCollector()
		mgr := NewTunnelManager(mockTunnelFactory(nil), collector)

		_ = mgr.Add(8080, "web", "", "")

		managed := mgr.getManagedTunnel(8080)
		now := time.Now()
		managed.Callbacks.OnDisconnected(now)

		msgs := collector.Messages()
		found := false
		for _, msg := range msgs {
			if discMsg, ok := msg.(TunnelDisconnectedMsg); ok && discMsg.Port == 8080 {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected TunnelDisconnectedMsg for port 8080")
		}
	})

	t.Run("reconnecting callback sends TunnelReconnectingMsg", func(t *testing.T) {
		collector := newMsgCollector()
		mgr := NewTunnelManager(mockTunnelFactory(nil), collector)

		_ = mgr.Add(8080, "web", "", "")

		managed := mgr.getManagedTunnel(8080)
		managed.Callbacks.OnReconnecting(3, 4*time.Second)

		msgs := collector.Messages()
		found := false
		for _, msg := range msgs {
			if reconMsg, ok := msg.(TunnelReconnectingMsg); ok {
				if reconMsg.Port == 8080 && reconMsg.Attempt == 3 {
					found = true
					break
				}
			}
		}
		if !found {
			t.Error("expected TunnelReconnectingMsg for port 8080 with attempt 3")
		}
	})

	t.Run("request callback sends TunnelRequestMsg", func(t *testing.T) {
		collector := newMsgCollector()
		mgr := NewTunnelManager(mockTunnelFactory(nil), collector)

		_ = mgr.Add(8080, "web", "", "")

		managed := mgr.getManagedTunnel(8080)
		managed.Callbacks.OnRequest("GET", "/api/users", 200, 50*time.Millisecond)

		msgs := collector.Messages()
		found := false
		for _, msg := range msgs {
			if reqMsg, ok := msg.(TunnelRequestMsg); ok {
				if reqMsg.Port == 8080 && reqMsg.Method == "GET" && reqMsg.Path == "/api/users" && reqMsg.Status == 200 {
					found = true
					break
				}
			}
		}
		if !found {
			t.Error("expected TunnelRequestMsg for port 8080")
		}
	})
}

func TestManagerReconnectSubdomainChange(t *testing.T) {
	t.Run("subdomain change on reconnect resets stats", func(t *testing.T) {
		collector := newMsgCollector()
		mgr := NewTunnelManager(mockTunnelFactory(nil), collector)

		_ = mgr.Add(8080, "web", "", "")

		managed := mgr.getManagedTunnel(8080)
		// Simulate initial connection: set subdomain
		managed.Callbacks.OnConnected("old-sub", "https://old-sub.justtunnel.dev", "localhost:8080", false)

		// Record some stats
		managed.Stats.Record(RequestEntry{
			Method:     "GET",
			Path:       "/test",
			StatusCode: 200,
			Duration:   10 * time.Millisecond,
			Timestamp:  time.Now(),
		})

		if managed.Stats.TotalCount() != 1 {
			t.Fatalf("expected 1 request recorded, got %d", managed.Stats.TotalCount())
		}

		// Simulate reconnect with a DIFFERENT subdomain
		managed.handleReconnected("new-sub", "old-sub", "https://new-sub.justtunnel.dev", true)

		// Stats should be reset
		if managed.Stats.TotalCount() != 0 {
			t.Errorf("expected stats reset (0 requests), got %d", managed.Stats.TotalCount())
		}

		// Check that TunnelReconnectedMsg was sent
		msgs := collector.Messages()
		found := false
		for _, msg := range msgs {
			if reconMsg, ok := msg.(TunnelReconnectedMsg); ok {
				if reconMsg.Port == 8080 && reconMsg.SubdomainChanged && reconMsg.NewSubdomain == "new-sub" {
					found = true
					break
				}
			}
		}
		if !found {
			t.Error("expected TunnelReconnectedMsg with SubdomainChanged=true")
		}
	})

	t.Run("same subdomain on reconnect preserves stats", func(t *testing.T) {
		collector := newMsgCollector()
		mgr := NewTunnelManager(mockTunnelFactory(nil), collector)

		_ = mgr.Add(8080, "web", "", "")

		managed := mgr.getManagedTunnel(8080)
		managed.Callbacks.OnConnected("same-sub", "https://same-sub.justtunnel.dev", "localhost:8080", false)

		managed.Stats.Record(RequestEntry{
			Method:     "GET",
			Path:       "/test",
			StatusCode: 200,
			Duration:   10 * time.Millisecond,
			Timestamp:  time.Now(),
		})

		// Reconnect with the SAME subdomain
		managed.handleReconnected("same-sub", "same-sub", "https://same-sub.justtunnel.dev", false)

		// Stats should be preserved
		if managed.Stats.TotalCount() != 1 {
			t.Errorf("expected stats preserved (1 request), got %d", managed.Stats.TotalCount())
		}
	})
}

func TestManagerRemoveByIndexBounds(t *testing.T) {
	tests := []struct {
		name    string
		index   int
		wantErr string
	}{
		{"zero index", 0, "No tunnel at index 0. Use /list to see active tunnels."},
		{"negative index", -1, "No tunnel at index -1. Use /list to see active tunnels."},
		{"index beyond count", 3, "No tunnel at index 3. Use /list to see active tunnels."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := NewTunnelManager(mockTunnelFactory(nil), nil)
			_ = mgr.Add(8080, "web", "", "")
			_ = mgr.Add(3000, "api", "", "")

			err := mgr.RemoveByIndex(tt.index)
			if err == nil {
				t.Fatalf("expected error for index %d, got nil", tt.index)
			}
			if err.Error() != tt.wantErr {
				t.Errorf("error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestManagerRunErrorPropagation(t *testing.T) {
	t.Run("runner.Run error sends TunnelErrorMsg and sets StateError", func(t *testing.T) {
		collector := newMsgCollector()
		tunnelErr := fmt.Errorf("plan limit reached: upgrade to add more tunnels")
		mocks := make(map[int]*mockTunnel)
		mgr := NewTunnelManager(mockTunnelFactoryWithError(tunnelErr, mocks), collector)

		err := mgr.Add(8080, "web", "", "")
		if err != nil {
			t.Fatalf("Add(8080) returned unexpected error: %v", err)
		}

		// The mock returns the error immediately from Run(), so give the
		// goroutine a moment to propagate the error.
		time.Sleep(50 * time.Millisecond)

		// Verify that a TunnelErrorMsg was sent with the correct port and message
		msgs := collector.Messages()
		foundError := false
		for _, msg := range msgs {
			if errMsg, ok := msg.(TunnelErrorMsg); ok {
				if errMsg.Port == 8080 && errMsg.Message == tunnelErr.Error() {
					foundError = true
					break
				}
			}
		}
		if !foundError {
			t.Errorf("expected TunnelErrorMsg for port 8080 with message %q, got messages: %v", tunnelErr.Error(), msgs)
		}

		// Verify that the managed tunnel's state is set to StateError
		managed := mgr.getManagedTunnel(8080)
		if managed == nil {
			t.Fatal("expected managed tunnel for port 8080")
		}
		if managed.GetState() != StateError {
			t.Errorf("expected state %v, got %v", StateError, managed.GetState())
		}
	})

	t.Run("context cancellation error is not propagated as TunnelErrorMsg", func(t *testing.T) {
		collector := newMsgCollector()
		mocks := make(map[int]*mockTunnel)
		mgr := NewTunnelManager(mockTunnelFactory(mocks), collector)

		err := mgr.Add(8080, "web", "", "")
		if err != nil {
			t.Fatalf("Add(8080) returned unexpected error: %v", err)
		}

		// Remove the tunnel, which cancels the context
		err = mgr.RemoveByPort(8080)
		if err != nil {
			t.Fatalf("RemoveByPort(8080) returned unexpected error: %v", err)
		}

		time.Sleep(50 * time.Millisecond)

		// Verify no TunnelErrorMsg was sent (context.Canceled is not a real error)
		msgs := collector.Messages()
		for _, msg := range msgs {
			if errMsg, ok := msg.(TunnelErrorMsg); ok {
				t.Errorf("unexpected TunnelErrorMsg: %+v", errMsg)
			}
		}
	})
}

func TestManagerCallbackPasswordProtected(t *testing.T) {
	t.Run("connected callback with passwordProtected sets flag and sends it in message", func(t *testing.T) {
		collector := newMsgCollector()
		mgr := NewTunnelManager(mockTunnelFactory(nil), collector)

		_ = mgr.Add(8080, "web", "", "")

		managed := mgr.getManagedTunnel(8080)
		if managed == nil {
			t.Fatal("expected managed tunnel for port 8080")
		}

		// Simulate connection with password protection enabled
		managed.Callbacks.OnConnected("test-sub", "https://test-sub.justtunnel.dev", "localhost:8080", true)

		// Verify ManagedTunnel.PasswordProtected is set
		managed.mu.RLock()
		pwProtected := managed.PasswordProtected
		managed.mu.RUnlock()
		if !pwProtected {
			t.Error("expected PasswordProtected to be true on ManagedTunnel")
		}

		// Verify the TunnelConnectedMsg carries PasswordProtected
		msgs := collector.Messages()
		foundMsg := false
		for _, msg := range msgs {
			if connMsg, ok := msg.(TunnelConnectedMsg); ok {
				if connMsg.Port == 8080 && connMsg.PasswordProtected {
					foundMsg = true
					break
				}
			}
		}
		if !foundMsg {
			t.Error("expected TunnelConnectedMsg with PasswordProtected=true")
		}
	})

	t.Run("connected callback without password protection leaves flag false", func(t *testing.T) {
		collector := newMsgCollector()
		mgr := NewTunnelManager(mockTunnelFactory(nil), collector)

		_ = mgr.Add(8080, "web", "", "")

		managed := mgr.getManagedTunnel(8080)
		managed.Callbacks.OnConnected("test-sub", "https://test-sub.justtunnel.dev", "localhost:8080", false)

		managed.mu.RLock()
		pwProtected := managed.PasswordProtected
		managed.mu.RUnlock()
		if pwProtected {
			t.Error("expected PasswordProtected to be false")
		}

		msgs := collector.Messages()
		for _, msg := range msgs {
			if connMsg, ok := msg.(TunnelConnectedMsg); ok {
				if connMsg.Port == 8080 && connMsg.PasswordProtected {
					t.Error("expected TunnelConnectedMsg with PasswordProtected=false")
				}
			}
		}
	})
}

func TestManagedTunnelConcurrentAccess(t *testing.T) {
	// This test verifies that concurrent reads and writes to ManagedTunnel
	// fields don't race. Run with -race to detect data races.
	collector := newMsgCollector()
	mgr := NewTunnelManager(mockTunnelFactory(nil), collector)

	_ = mgr.Add(8080, "web", "", "")

	managed := mgr.getManagedTunnel(8080)
	if managed == nil {
		t.Fatal("expected managed tunnel for port 8080")
	}

	// Spawn concurrent writers (simulating callbacks)
	var writeWg sync.WaitGroup
	writeWg.Add(3)
	go func() {
		defer writeWg.Done()
		for iter := 0; iter < 100; iter++ {
			managed.Callbacks.OnConnected("sub", "https://sub.example.com", "localhost:8080", false)
		}
	}()
	go func() {
		defer writeWg.Done()
		for iter := 0; iter < 100; iter++ {
			managed.Callbacks.OnDisconnected(time.Now())
		}
	}()
	go func() {
		defer writeWg.Done()
		for iter := 0; iter < 100; iter++ {
			managed.Callbacks.OnReconnecting(iter, time.Second)
		}
	}()

	// Spawn concurrent readers (simulating TUI render loop)
	var readWg sync.WaitGroup
	readWg.Add(1)
	go func() {
		defer readWg.Done()
		for iter := 0; iter < 100; iter++ {
			_ = managed.GetState()
			_ = managed.GetSubdomain()
			_ = managed.GetPublicURL()
			_ = managed.GetConnectedAt()
			_ = managed.GetError()
		}
	}()

	writeWg.Wait()
	readWg.Wait()
}
