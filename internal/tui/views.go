package tui

import (
	"fmt"
	"strings"
	"time"
)

// renderListView renders the main list view showing all tunnels in a table.
func renderListView(model Model) string {
	var builder strings.Builder

	// Header
	builder.WriteString(renderHeader(model))
	builder.WriteString("\n\n")

	if len(model.tunnels) == 0 {
		builder.WriteString("  No active tunnels. Use /add <port> to start a tunnel.\n")
	} else {
		// Column headers
		headerLine := fmt.Sprintf("  %-3s %-15s %-15s %-35s %-10s %-6s",
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

			row := fmt.Sprintf("%-3d %-15s %s %-35s %-10s %-6d",
				entry.ID,
				truncateString(entry.Name, 15),
				styledStatus,
				truncateString(entry.PublicURL, 35),
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

	// Input bar
	builder.WriteString(renderInputBar(model))

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

	// Input bar
	builder.WriteString(renderInputBar(model))

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
