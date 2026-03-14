package tui

import (
	"fmt"
	"strings"
	"time"
)

// minHeightForInputBar is the minimum terminal height to show the command input bar.
// Below this threshold, only the tunnel list is displayed to conserve vertical space.
const minHeightForInputBar = 10

// fixedColumnsWidth is the total width consumed by non-URL columns in the list view.
// Layout: marker(2) + ID(3+1) + Name(15+1) + Status(15+1) + Local(10+1) + Reqs(6) = ~55
const fixedColumnsWidth = 55

// inputBarVisible reports whether the terminal is tall enough to show the input bar.
func inputBarVisible(terminalHeight int) bool {
	return terminalHeight >= minHeightForInputBar
}

// urlColumnWidth calculates the available width for the URL column based on terminal width.
// It subtracts fixed column space and clamps the result to a reasonable range.
func urlColumnWidth(terminalWidth int) int {
	available := terminalWidth - fixedColumnsWidth
	if available < 10 {
		available = 10
	}
	if available > 55 {
		available = 55
	}
	return available
}

// renderListView renders the main list view showing all tunnels in a table.
func renderListView(model Model) string {
	var builder strings.Builder

	// Header
	builder.WriteString(renderHeader(model))
	builder.WriteString("\n\n")

	urlWidth := urlColumnWidth(model.width)

	if len(model.tunnels) == 0 {
		builder.WriteString("  No active tunnels. Use /add <port> to start a tunnel.\n")
	} else {
		// Column format string — computed once, used for header and each row
		urlFmt := fmt.Sprintf("%%-%ds", urlWidth)

		// Column headers
		headerLine := fmt.Sprintf("  %-3s %-15s %-15s "+urlFmt+" %-10s %-6s",
			"#", "Name", "Status", "URL", "Local", "Reqs")
		builder.WriteString(styleColumnHeader.Render(headerLine))
		builder.WriteString("\n")

		// Tunnel rows
		for idx, entry := range model.tunnels {
			marker := "  "
			if idx == model.selectedIndex {
				marker = styleSelected.Render("> ")
			}

			styledStatus := stateStyle(entry.State).Render(fmt.Sprintf("%-15s", stateLabel(entry.State)))

			row := fmt.Sprintf("%-3d %-15s %s "+urlFmt+" %-10s %-6d",
				entry.ID,
				truncateString(entry.Name, 15),
				styledStatus,
				truncateString(entry.PublicURL, urlWidth),
				fmt.Sprintf(":%d", entry.Port),
				entry.Requests,
			)

			builder.WriteString(marker)
			builder.WriteString(row)
			builder.WriteString("\n")
		}
	}

	builder.WriteString("\n")

	// Error message if present
	if model.errorMessage != "" {
		builder.WriteString(styleError.Render("  Error: " + model.errorMessage))
		builder.WriteString("\n")
	}

	// Input bar — only shown when terminal is tall enough
	if inputBarVisible(model.height) {
		builder.WriteString(renderInputBar(model))
	}

	return builder.String()
}

// renderDetailView renders the detailed view for the currently selected tunnel.
func renderDetailView(model Model) string {
	if model.selectedIndex >= len(model.tunnels) {
		return renderListView(model)
	}

	entry := model.tunnels[model.selectedIndex]

	var builder strings.Builder

	// Header
	builder.WriteString(renderHeader(model))
	builder.WriteString("\n\n")

	// Back hint
	builder.WriteString("  Press Esc to return to list\n\n")

	// Tunnel detail metadata
	builder.WriteString(fmt.Sprintf("  %s  %s\n",
		styleDetailLabel.Render("Public URL:"),
		styleDetailValue.Render(entry.PublicURL)))

	builder.WriteString(fmt.Sprintf("  %s  :%d\n",
		styleDetailLabel.Render("Local:"),
		entry.Port))

	builder.WriteString(fmt.Sprintf("  %s  %s\n",
		styleDetailLabel.Render("Subdomain:"),
		entry.Subdomain))

	statusText := stateStyle(entry.State).Render(stateLabel(entry.State))
	builder.WriteString(fmt.Sprintf("  %s  %s\n",
		styleDetailLabel.Render("Status:"),
		statusText))

	uptime := time.Duration(0)
	if !entry.ConnectedAt.IsZero() {
		uptime = time.Since(entry.ConnectedAt)
	}
	builder.WriteString(fmt.Sprintf("  %s  %s\n",
		styleDetailLabel.Render("Uptime:"),
		formatUptime(uptime)))

	builder.WriteString(fmt.Sprintf("  %s  %d\n",
		styleDetailLabel.Render("Total Requests:"),
		entry.Requests))

	builder.WriteString(fmt.Sprintf("  %s  %.1f\n",
		styleDetailLabel.Render("Avg req/sec:"),
		entry.AvgReqSec))

	builder.WriteString("\n")

	// Recent requests table header
	builder.WriteString(styleColumnHeader.Render("  Recent Requests"))
	builder.WriteString("\n")
	builder.WriteString("  (no requests yet)\n")

	builder.WriteString("\n")

	// Error message if present
	if model.errorMessage != "" {
		builder.WriteString(styleError.Render("  Error: " + model.errorMessage))
		builder.WriteString("\n")
	}

	// Input bar — only shown when terminal is tall enough
	if inputBarVisible(model.height) {
		builder.WriteString(renderInputBar(model))
	}

	return builder.String()
}

// renderHeader renders the top header with logo and plan quota.
func renderHeader(model Model) string {
	header := styleHeader.Render("justtunnel")
	quota := stylePlanQuota.Render(fmt.Sprintf("  %d/%d tunnels (%s)",
		len(model.tunnels), model.planInfo.MaxTunnels, model.planInfo.Name))
	return header + quota
}

// renderInputBar renders the bottom command input prompt.
func renderInputBar(model Model) string {
	prompt := styleInputBar.Render("> ")
	return prompt + model.inputBuffer
}

// truncateString truncates a string to maxLen runes, adding "..." if truncated.
func truncateString(text string, maxLen int) string {
	runes := []rune(text)
	if len(runes) <= maxLen {
		return text
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

// formatUptime converts a duration into a human-readable uptime string.
func formatUptime(duration time.Duration) string {
	if duration < time.Minute {
		return fmt.Sprintf("%ds", int(duration.Seconds()))
	}
	if duration < time.Hour {
		minutes := int(duration.Minutes())
		seconds := int(duration.Seconds()) % 60
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	hours := int(duration.Hours())
	minutes := int(duration.Minutes()) % 60
	seconds := int(duration.Seconds()) % 60
	return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
}

// NonTTYEvent represents a tunnel event formatted for non-TTY (piped) output.
// Each event produces a single line prefixed with [name:port].
type NonTTYEvent struct {
	TunnelName string
	Port       int
	EventType  string // "connected", "request", "disconnected", "reconnecting", "error"
	URL        string // for connected events
	Method     string // for request events
	Path       string // for request events
	Status     int    // for request events
	Latency    time.Duration
	Detail     string // free-form detail for reconnecting/error events
}

// FormatNonTTYEvent formats a tunnel event as a single line for non-TTY output.
// Format: [name:port] event_details
//
// Examples:
//
//	[frontend:3000] connected https://abc123.justtunnel.dev
//	[api:8080] GET /api/users 200 12ms
//	[frontend:3000] disconnected
//	[api:8080] reconnecting attempt 3
func FormatNonTTYEvent(event NonTTYEvent) string {
	prefix := fmt.Sprintf("[%s:%d]", event.TunnelName, event.Port)

	switch event.EventType {
	case "connected":
		return fmt.Sprintf("%s connected %s", prefix, event.URL)

	case "request":
		return fmt.Sprintf("%s %s %s %d %s", prefix,
			event.Method, event.Path, event.Status,
			event.Latency.Round(time.Millisecond).String())

	case "disconnected":
		return fmt.Sprintf("%s disconnected", prefix)

	case "reconnecting":
		if event.Detail != "" {
			return fmt.Sprintf("%s reconnecting %s", prefix, event.Detail)
		}
		return fmt.Sprintf("%s reconnecting", prefix)

	case "error":
		if event.Detail != "" {
			return fmt.Sprintf("%s error %s", prefix, event.Detail)
		}
		return fmt.Sprintf("%s error", prefix)

	default:
		if event.Detail != "" {
			return fmt.Sprintf("%s %s %s", prefix, event.EventType, event.Detail)
		}
		return fmt.Sprintf("%s %s", prefix, event.EventType)
	}
}
