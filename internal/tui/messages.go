package tui

import "time"

// TickMsg is sent every second to refresh uptime displays and time-based UI.
type TickMsg time.Time

// TunnelConnectedMsg indicates a tunnel has successfully connected.
type TunnelConnectedMsg struct {
	Port              int
	Subdomain         string
	PublicURL         string
	PasswordProtected bool
}

// TunnelDisconnectedMsg indicates a tunnel has lost its connection.
type TunnelDisconnectedMsg struct {
	Port      int
	Timestamp time.Time
}

// TunnelReconnectingMsg indicates a tunnel is attempting to reconnect.
type TunnelReconnectingMsg struct {
	Port    int
	Attempt int
	Backoff time.Duration
}

// TunnelRequestMsg carries a proxied request event for display in the TUI.
type TunnelRequestMsg struct {
	Port    int
	Method  string
	Path    string
	Status  int
	Latency time.Duration
}

// TunnelReconnectedMsg indicates a tunnel has successfully reconnected.
type TunnelReconnectedMsg struct {
	Port             int
	SubdomainChanged bool
	NewSubdomain     string
}

// TunnelErrorMsg carries an error event for a specific tunnel.
type TunnelErrorMsg struct {
	Port    int
	Message string
}

// ConfigChangedMsg indicates the config file was reloaded and tunnels need updating.
type ConfigChangedMsg struct {
	ToAdd    []TunnelPreset
	ToRemove []int
}

// ConfigReloadErrorMsg indicates the config file could not be reloaded (invalid YAML, missing file, etc.).
// Existing tunnels are left untouched.
type ConfigReloadErrorMsg struct {
	Error string
}

// TunnelPreset holds the configuration for a tunnel from a config file.
type TunnelPreset struct {
	Port      int    `yaml:"port"`
	Name      string `yaml:"name,omitempty"`
	Subdomain string `yaml:"subdomain,omitempty"`
	Password  string `yaml:"password,omitempty"`
}
