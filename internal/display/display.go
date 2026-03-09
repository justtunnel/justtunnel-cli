package display

import (
	"fmt"
	"io"
	"os"
	"sort"
	"time"
)

var output io.Writer = os.Stderr

// SetOutput overrides the default output writer (os.Stderr). Useful for testing.
// Passing nil resets the output to os.Stderr.
func SetOutput(writer io.Writer) {
	if writer == nil {
		output = os.Stderr
		return
	}
	output = writer
}

func LogRequest(method, path string, status int, latency time.Duration) {
	method = sanitize(method)
	path = sanitize(path)
	colorDim.Fprintf(output, "  %-7s", method)
	fmt.Fprintf(output, " %-30s ", path)

	statusColor := colorGreen
	switch {
	case status >= 500:
		statusColor = colorRed
	case status >= 300:
		statusColor = colorYellow
	}
	statusColor.Fprintf(output, "%d", status)

	colorDim.Fprintf(output, "  %s\n", latency.Round(time.Millisecond))
}

// LogRequestDetail prints debug-level headers and a truncated body preview.
func LogRequestDetail(label string, headers map[string][]string, body []byte) {
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
