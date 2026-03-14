package tui

import (
	"context"
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
type ManagedTunnel struct {
	Name          string
	Port          int
	Subdomain     string
	LastSubdomain string
	PublicURL     string
	State         TunnelState
	Error         string
	ConnectedAt   time.Time
	Stats         *RequestStats
	Source        string
	Callbacks     TunnelCallbacks

	runner TunnelRunner
	cancel context.CancelFunc
	sender MessageSender
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
			managed.Subdomain = sub
			managed.PublicURL = tunnelURL
			managed.State = StateConnected
			managed.ConnectedAt = time.Now()
			if sender != nil {
				sender.Send(TunnelConnectedMsg{
					Port:      port,
					Subdomain: sub,
					PublicURL: tunnelURL,
				})
			}
		},
		OnDisconnected: func(timestamp time.Time) {
			managed.State = StateDisconnected
			if sender != nil {
				sender.Send(TunnelDisconnectedMsg{
					Port:      port,
					Timestamp: timestamp,
				})
			}
		},
		OnReconnecting: func(attempt int, backoff time.Duration) {
			managed.State = StateReconnecting
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
	m.LastSubdomain = previousSubdomain
	m.Subdomain = subdomain
	m.PublicURL = tunnelURL
	m.State = StateConnected
	m.ConnectedAt = time.Now()

	if subdomainChanged {
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
func (m *ManagedTunnel) start() {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	go func() {
		_ = m.runner.Run(ctx)
	}()
}

// shutdown stops the tunnel runner with a timeout and cancels the context.
func (m *ManagedTunnel) shutdown(timeout time.Duration) {
	m.runner.Shutdown(timeout)
	if m.cancel != nil {
		m.cancel()
	}
}
