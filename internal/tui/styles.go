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

// requestStatusStyle returns a style colored by HTTP status code range.
func requestStatusStyle(statusCode int) lipgloss.Style {
	switch {
	case statusCode >= 500:
		return lipgloss.NewStyle().Foreground(colorRed)
	case statusCode >= 400:
		return lipgloss.NewStyle().Foreground(colorYellow)
	case statusCode >= 300:
		return lipgloss.NewStyle().Foreground(colorYellow)
	default:
		return lipgloss.NewStyle().Foreground(colorGreen)
	}
}

// stateStyle returns the appropriate Lip Gloss style for a tunnel state.
func stateStyle(state TunnelState) lipgloss.Style {
	switch state {
	case StateConnected:
		return lipgloss.NewStyle().Foreground(colorGreen)
	case StateConnecting, StateReconnecting:
		return lipgloss.NewStyle().Foreground(colorYellow)
	case StateDisconnected, StateError:
		return lipgloss.NewStyle().Foreground(colorRed)
	default:
		return lipgloss.NewStyle().Foreground(colorDim)
	}
}

// stateLabel returns a text label for a tunnel state, for accessibility (NFR-3.1).
func stateLabel(state TunnelState) string {
	return "[" + state.String() + "]"
}
