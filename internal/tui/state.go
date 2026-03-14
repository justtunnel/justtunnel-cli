package tui

// TunnelState represents the current lifecycle state of a tunnel connection.
type TunnelState int

const (
	// StateConnecting indicates the tunnel is establishing its initial connection.
	StateConnecting TunnelState = iota
	// StateConnected indicates the tunnel is actively connected and forwarding traffic.
	StateConnected
	// StateReconnecting indicates the tunnel lost connection and is attempting to reconnect.
	StateReconnecting
	// StateDisconnected indicates the tunnel has been intentionally disconnected.
	StateDisconnected
	// StateError indicates the tunnel encountered a fatal error and cannot recover.
	StateError
)

// String returns a human-readable label for the tunnel state.
func (state TunnelState) String() string {
	switch state {
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	case StateReconnecting:
		return "reconnecting"
	case StateDisconnected:
		return "disconnected"
	case StateError:
		return "error"
	default:
		return "unknown"
	}
}
