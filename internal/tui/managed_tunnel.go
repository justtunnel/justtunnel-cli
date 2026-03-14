package tui

import (
	"context"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TunnelRunner is the interface that a real tunnel or a mock must implement
// for the manager to control its lifecycle.
type TunnelRunner interface {
	Run(ctx context.Context) error
	Shutdown(timeout time.Duration)
}

// TunnelCallbacks holds the callback functions that the manager wires
// into each tunnel to bridge lifecycle events to Bubble Tea messages.
type TunnelCallbacks struct {
	OnConnected    func(subdomain, url, localTarget string)
	OnDisconnected func(timestamp time.Time)
	OnReconnecting func(attempt int, backoff time.Duration)
	OnReconnected  func(subdomain, previousSubdomain, tunnelURL string, subdomainChanged bool)
	OnRequest      func(method, path string, status int, latency time.Duration)
}

// TunnelFactory creates a TunnelRunner given the port, name, subdomain, and callbacks.
// In production this creates a real tunnel.Tunnel; in tests it returns a mock.
type TunnelFactory func(port int, name string, subdomain string, callbacks TunnelCallbacks) TunnelRunner

// MessageSender abstracts tea.Program.Send() so the manager can bridge
// tunnel callbacks to the TUI without depending on a real Bubble Tea program.
type MessageSender interface {
	Send(msg tea.Msg)
}

// ManagedTunnel wraps a TunnelRunner with TUI metadata for display and coordination.
// Fields that are written by callbacks from the tunnel goroutine are protected by mu.
// Use the getter methods (GetState, GetSubdomain, etc.) for concurrent reads.
type ManagedTunnel struct {
	// Immutable fields — safe to read without lock.
	Name   string
	Port   int
	Source string

	// mu protects mutable fields written by callbacks from the tunnel goroutine.
	mu            sync.RWMutex
	Subdomain     string
	LastSubdomain string
	PublicURL     string
	State         TunnelState
	Error         string
	ConnectedAt   time.Time
	Stats         *RequestStats
	Callbacks     TunnelCallbacks

	runner TunnelRunner
	cancel context.CancelFunc
	sender MessageSender
}

// GetState returns the current tunnel state (thread-safe).
func (m *ManagedTunnel) GetState() TunnelState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.State
}

// GetSubdomain returns the current subdomain (thread-safe).
func (m *ManagedTunnel) GetSubdomain() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.Subdomain
}

// GetPublicURL returns the current public URL (thread-safe).
func (m *ManagedTunnel) GetPublicURL() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.PublicURL
}

// GetConnectedAt returns the time the tunnel connected (thread-safe).
func (m *ManagedTunnel) GetConnectedAt() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ConnectedAt
}

// GetError returns the error message if the tunnel is in StateError (thread-safe).
func (m *ManagedTunnel) GetError() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.Error
}

// newManagedTunnel creates a ManagedTunnel with callbacks wired to send messages
// through the given MessageSender. The factory creates the underlying TunnelRunner.
func newManagedTunnel(
	port int,
	name string,
	subdomain string,
	factory TunnelFactory,
	sender MessageSender,
) *ManagedTunnel {
	managed := &ManagedTunnel{
		Name:   name,
		Port:   port,
		State:  StateConnecting,
		Stats:  NewRequestStats(),
		sender: sender,
	}

	callbacks := TunnelCallbacks{
		OnConnected: func(sub, tunnelURL, localTarget string) {
			managed.mu.Lock()
			managed.Subdomain = sub
			managed.PublicURL = tunnelURL
			managed.State = StateConnected
			managed.ConnectedAt = time.Now()
			managed.mu.Unlock()
			if sender != nil {
				sender.Send(TunnelConnectedMsg{
					Port:      port,
					Subdomain: sub,
					PublicURL: tunnelURL,
				})
			}
		},
		OnDisconnected: func(timestamp time.Time) {
			managed.mu.Lock()
			managed.State = StateDisconnected
			managed.mu.Unlock()
			if sender != nil {
				sender.Send(TunnelDisconnectedMsg{
					Port:      port,
					Timestamp: timestamp,
				})
			}
		},
		OnReconnecting: func(attempt int, backoff time.Duration) {
			managed.mu.Lock()
			managed.State = StateReconnecting
			managed.mu.Unlock()
			if sender != nil {
				sender.Send(TunnelReconnectingMsg{
					Port:    port,
					Attempt: attempt,
					Backoff: backoff,
				})
			}
		},
		OnReconnected: func(sub, previousSub, tunnelURL string, subdomainChanged bool) {
			managed.handleReconnected(sub, previousSub, tunnelURL, subdomainChanged)
		},
		OnRequest: func(method, path string, status int, latency time.Duration) {
			// Stats has its own internal mutex, no need to hold managed.mu here.
			managed.Stats.Record(RequestEntry{
				Method:     method,
				Path:       path,
				StatusCode: status,
				Duration:   latency,
				Timestamp:  time.Now(),
			})
			if sender != nil {
				sender.Send(TunnelRequestMsg{
					Port:    port,
					Method:  method,
					Path:    path,
					Status:  status,
					Latency: latency,
				})
			}
		},
	}

	managed.Callbacks = callbacks
	managed.runner = factory(port, name, subdomain, callbacks)

	return managed
}

// handleReconnected processes a reconnection event, resetting stats if the
// subdomain changed (FR-5.3) and sending a TunnelReconnectedMsg.
func (m *ManagedTunnel) handleReconnected(subdomain, previousSubdomain, tunnelURL string, subdomainChanged bool) {
	m.mu.Lock()
	m.LastSubdomain = previousSubdomain
	m.Subdomain = subdomain
	m.PublicURL = tunnelURL
	m.State = StateConnected
	m.ConnectedAt = time.Now()
	m.mu.Unlock()

	if subdomainChanged {
		// Stats has its own internal mutex.
		m.Stats.Reset()
	}

	if m.sender != nil {
		m.sender.Send(TunnelReconnectedMsg{
			Port:             m.Port,
			SubdomainChanged: subdomainChanged,
			NewSubdomain:     subdomain,
		})
	}
}

// start launches the tunnel's Run method in a background goroutine.
// If Run returns a non-context error, the tunnel state is set to StateError
// and a TunnelErrorMsg is sent to the TUI.
func (m *ManagedTunnel) start() {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	go func() {
		runErr := m.runner.Run(ctx)
		if runErr != nil && ctx.Err() == nil {
			m.mu.Lock()
			m.State = StateError
			m.Error = runErr.Error()
			m.mu.Unlock()
			if m.sender != nil {
				m.sender.Send(TunnelErrorMsg{
					Port:    m.Port,
					Message: runErr.Error(),
				})
			}
		}
	}()
}

// shutdown stops the tunnel runner with a timeout and cancels the context.
func (m *ManagedTunnel) shutdown(timeout time.Duration) {
	m.runner.Shutdown(timeout)
	if m.cancel != nil {
		m.cancel()
	}
}
