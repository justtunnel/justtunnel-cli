package display

import (
	"fmt"
	"time"
)

// PrintDisconnected prints a timestamped disconnection notice.
func PrintDisconnected(timestamp time.Time) {
	colorYellow.Fprintf(output, "  ⚠ Disconnected")
	colorDim.Fprintf(output, " at %s\n", timestamp.Format("15:04:05"))
}

// PrintReconnected prints reconnection details including subdomain status and downtime.
func PrintReconnected(subdomain, previousSubdomain, tunnelURL, localTarget string, subdomainChanged bool, downtime time.Duration) {
	subdomain = sanitize(subdomain)
	previousSubdomain = sanitize(previousSubdomain)
	tunnelURL = sanitize(tunnelURL)
	localTarget = sanitize(localTarget)

	roundedDowntime := downtime.Round(time.Second)

	colorGreen.Fprintf(output, "  ✓ Reconnected")
	colorDim.Fprintf(output, " after %s\n", roundedDowntime)

	if subdomainChanged {
		colorYellow.Fprintf(output, "    ⚠ Subdomain changed: ")
		fmt.Fprintf(output, "%s -> %s. Update your URLs.\n", previousSubdomain, subdomain)
	}

	colorCyan.Fprintf(output, "    %-12s", "Forwarding:")
	colorWhite.Fprintf(output, " %s", tunnelURL)
	colorDim.Fprintf(output, " -> ")
	colorWhite.Fprintf(output, "%s\n", localTarget)

	if !subdomainChanged {
		colorCyan.Fprintf(output, "    %-12s", "Subdomain:")
		colorWhite.Fprintf(output, " %s", subdomain)
		colorDim.Fprintf(output, " (preserved)\n")
	}
}
