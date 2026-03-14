package tui

import (
	"fmt"
	"sync"
	"time"
)

const (
	// minPort is the lowest valid port number.
	minPort = 1
	// maxPort is the highest valid port number.
	maxPort = 65535
	// shutdownTimeout is the per-tunnel shutdown timeout.
	shutdownTimeout = 5 * time.Second
)

// TunnelManager coordinates multiple tunnel instances, providing O(1) lookup
// by port and stable insertion-order iteration for display indexing.
type TunnelManager struct {
	mu sync.RWMutex

	// tunnelsByPort provides O(1) lookup by port number.
	tunnelsByPort map[int]*ManagedTunnel

	// insertionOrder maintains the order in which tunnels were added,
	// enabling stable 1-based indexing for the TUI.
	insertionOrder []int

	factory TunnelFactory
	sender  MessageSender
}

// NewTunnelManager creates a TunnelManager with the given factory and message sender.
// The factory creates TunnelRunner instances; the sender bridges callbacks to the TUI.
func NewTunnelManager(factory TunnelFactory, sender MessageSender) *TunnelManager {
	return &TunnelManager{
		tunnelsByPort:  make(map[int]*ManagedTunnel),
		insertionOrder: make([]int, 0),
		factory:        factory,
		sender:         sender,
	}
}

// Add creates and starts a new tunnel on the given port.
// Returns an error if the port is invalid or already in use.
func (m *TunnelManager) Add(port int, name string, subdomain string) error {
	if port < minPort || port > maxPort {
		return fmt.Errorf("invalid port %d: must be between %d and %d", port, minPort, maxPort)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.tunnelsByPort[port]; exists {
		return fmt.Errorf("tunnel already running on port %d", port)
	}

	managed := newManagedTunnel(port, name, subdomain, m.factory, m.sender)
	m.tunnelsByPort[port] = managed
	m.insertionOrder = append(m.insertionOrder, port)

	managed.start()

	return nil
}

// RemoveByIndex removes the tunnel at the given 1-based index.
// Returns an error if the index is out of bounds.
func (m *TunnelManager) RemoveByIndex(index int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if index < 1 || index > len(m.insertionOrder) {
		return fmt.Errorf("No tunnel at index %d. Use /list to see active tunnels.", index)
	}

	port := m.insertionOrder[index-1]
	return m.removeLocked(port, index-1)
}

// RemoveByPort removes the tunnel running on the given port.
// Returns an error if no tunnel is running on that port.
func (m *TunnelManager) RemoveByPort(port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.tunnelsByPort[port]; !exists {
		return fmt.Errorf("no tunnel running on port %d", port)
	}

	// Find the index in insertion order
	for idx, orderedPort := range m.insertionOrder {
		if orderedPort == port {
			return m.removeLocked(port, idx)
		}
	}

	// Should not be reachable if tunnelsByPort and insertionOrder are in sync
	return fmt.Errorf("internal error: port %d in map but not in insertion order", port)
}

// removeLocked removes a tunnel by port and slice index. Must be called with mu held.
func (m *TunnelManager) removeLocked(port int, sliceIndex int) error {
	managed, exists := m.tunnelsByPort[port]
	if !exists {
		return fmt.Errorf("no tunnel running on port %d", port)
	}

	// Shutdown the tunnel in the background to avoid holding the lock
	go managed.shutdown(shutdownTimeout)

	delete(m.tunnelsByPort, port)
	m.insertionOrder = append(m.insertionOrder[:sliceIndex], m.insertionOrder[sliceIndex+1:]...)

	return nil
}

// Shutdown stops all tunnels concurrently and clears the manager state.
func (m *TunnelManager) Shutdown() {
	m.mu.Lock()
	tunnels := make([]*ManagedTunnel, 0, len(m.tunnelsByPort))
	for _, managed := range m.tunnelsByPort {
		tunnels = append(tunnels, managed)
	}
	m.tunnelsByPort = make(map[int]*ManagedTunnel)
	m.insertionOrder = make([]int, 0)
	m.mu.Unlock()

	// Shutdown all tunnels concurrently
	var shutdownWg sync.WaitGroup
	for _, managed := range tunnels {
		shutdownWg.Add(1)
		go func(tun *ManagedTunnel) {
			defer shutdownWg.Done()
			tun.shutdown(shutdownTimeout)
		}(managed)
	}
	shutdownWg.Wait()
}

// Count returns the number of active tunnels.
func (m *TunnelManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tunnelsByPort)
}

// Tunnels returns a snapshot of all managed tunnels in stable insertion order.
// The returned slice is safe to read without holding the manager lock.
func (m *TunnelManager) Tunnels() []*ManagedTunnel {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*ManagedTunnel, 0, len(m.insertionOrder))
	for _, port := range m.insertionOrder {
		if managed, exists := m.tunnelsByPort[port]; exists {
			result = append(result, managed)
		}
	}
	return result
}

// getManagedTunnel returns the ManagedTunnel for the given port, or nil if not found.
// This is primarily used in tests to access callbacks directly.
func (m *TunnelManager) getManagedTunnel(port int) *ManagedTunnel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tunnelsByPort[port]
}
