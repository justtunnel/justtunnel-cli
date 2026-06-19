package tui

import "github.com/charmbracelet/lipgloss"

// Color constants used across the TUI.
var (
	colorGreen  = lipgloss.Color("#00FF00")
	colorYellow = lipgloss.Color("#FFFF00")
	colorRed    = lipgloss.Color("#FF0000")
	colorCyan   = lipgloss.Color("#00FFFF")
	colorDim    = lipgloss.Color("#666666")
	colorWhite  = lipgloss.Color("#FFFFFF")
)

// Style definitions for TUI rendering.
var (
	// styleHeader is the style for the top header bar.
	styleHeader = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorCyan)

	// stylePlanQuota is the style for the plan usage indicator (e.g., "2/5 tunnels (Pro)").
	stylePlanQuota = lipgloss.NewStyle().
			Foreground(colorDim)

	// styleSelected is the style for the currently selected row marker.
	styleSelected = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorCyan)

	// styleColumnHeader is the style for table column headers.
	styleColumnHeader = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorWhite)

	// styleInputBar is the style for the bottom command input prompt.
	styleInputBar = lipgloss.NewStyle().
			Foreground(colorDim)

	// styleError is the style for error messages displayed in the TUI.
	styleError = lipgloss.NewStyle().
			Foreground(colorRed)

	// styleDetailLabel is the style for labels in the detail view.
	styleDetailLabel = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorWhite)

	// styleDetailValue is the style for values in the detail view.
	styleDetailValue = lipgloss.NewStyle().
				Foreground(colorCyan)

	// styleDim is the style for dimmed/secondary text.
	styleDim = lipgloss.NewStyle().
			Foreground(colorDim)
)

// Status- and state-colored styles, pre-allocated once at package init. These
// were previously rebuilt on every render via lipgloss.NewStyle(); the detail
// view calls requestStatusStyle per recent-request row, so allocating fresh
// styles each frame churned the heap for no benefit. Styles are immutable
// values, so sharing one instance per color is safe.
var (
	styleStatusServerError = lipgloss.NewStyle().Foreground(colorRed)    // 5xx
	styleStatusClientError = lipgloss.NewStyle().Foreground(colorYellow) // 4xx
	styleStatusRedirect    = lipgloss.NewStyle().Foreground(colorCyan)   // 3xx — informational, not an error
	styleStatusSuccess     = lipgloss.NewStyle().Foreground(colorGreen)  // 2xx and below

	styleStateConnected    = lipgloss.NewStyle().Foreground(colorGreen)
	styleStateTransitional = lipgloss.NewStyle().Foreground(colorYellow) // connecting / reconnecting
	styleStateDown         = lipgloss.NewStyle().Foreground(colorRed)    // disconnected / error
	styleStateUnknown      = lipgloss.NewStyle().Foreground(colorDim)
)

// requestStatusStyle returns a style colored by HTTP status code range. 3xx
// redirects get their own color so they aren't mistaken for 4xx errors.
func requestStatusStyle(statusCode int) lipgloss.Style {
	switch {
	case statusCode >= 500:
		return styleStatusServerError
	case statusCode >= 400:
		return styleStatusClientError
	case statusCode >= 300:
		return styleStatusRedirect
	default:
		return styleStatusSuccess
	}
}

// stateStyle returns the appropriate Lip Gloss style for a tunnel state.
func stateStyle(state TunnelState) lipgloss.Style {
	switch state {
	case StateConnected:
		return styleStateConnected
	case StateConnecting, StateReconnecting:
		return styleStateTransitional
	case StateDisconnected, StateError:
		return styleStateDown
	default:
		return styleStateUnknown
	}
}

// stateLabel returns a text label for a tunnel state, for accessibility (NFR-3.1).
func stateLabel(state TunnelState) string {
	return "[" + state.String() + "]"
}
