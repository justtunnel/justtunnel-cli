package display

import (
	"fmt"
	"os"
	"sort"
	"time"
)

func PrintBanner(subdomain, url, localTarget string) {
	output := os.Stderr
	fmt.Fprintln(output)
	fmt.Fprintln(output, "  justtunnel")
	fmt.Fprintln(output)
	fmt.Fprintf(output, "  %-14s %s → %s\n", "Forwarding:", url, localTarget)
	fmt.Fprintf(output, "  %-14s %s\n", "Subdomain:", subdomain)
	fmt.Fprintln(output)
}

func LogRequest(method, path string, status int, latency time.Duration) {
	fmt.Fprintf(os.Stderr, "  %-7s %-30s %d  %s\n", method, path, status, latency.Round(time.Millisecond))
}

func LogReconnecting(attempt int, backoff time.Duration) {
	fmt.Fprintf(os.Stderr, "  reconnecting (attempt %d, backoff %s)...\n", attempt, backoff)
}

func LogReconnected() {
	fmt.Fprintln(os.Stderr, "  reconnected")
}

// LogRequestDetail prints debug-level headers and a truncated body preview.
func LogRequestDetail(label string, headers map[string][]string, body []byte) {
	output := os.Stderr
	fmt.Fprintf(output, "    %s Headers:\n", label)

	keys := make([]string, 0, len(headers))
	for headerName := range headers {
		keys = append(keys, headerName)
	}
	sort.Strings(keys)

	for _, headerName := range keys {
		for _, headerValue := range headers[headerName] {
			fmt.Fprintf(output, "      %s: %s\n", headerName, headerValue)
		}
	}

	if len(body) > 0 {
		fmt.Fprintf(output, "    %s Body: %s\n", label, bodyPreview(body, 512))
	}
}

func bodyPreview(data []byte, maxLen int) string {
	if len(data) <= maxLen {
		return string(data)
	}
	return fmt.Sprintf("%s... (%d bytes total)", string(data[:maxLen]), len(data))
}
