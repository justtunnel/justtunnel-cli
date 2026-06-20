package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// requestStatusStyle maps HTTP status ranges to distinct colors. The 3xx arm
// must not collapse into the 4xx arm (redirects looking like errors was the
// documented UX bug this guards against), so each range maps to its own
// pre-allocated style.
func TestRequestStatusStyle(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		want       lipgloss.TerminalColor // expected foreground color
	}{
		{name: "5xx server error", statusCode: 500, want: colorRed},
		{name: "503 server error", statusCode: 503, want: colorRed},
		{name: "4xx client error", statusCode: 404, want: colorYellow},
		{name: "400 client error", statusCode: 400, want: colorYellow},
		{name: "3xx redirect", statusCode: 301, want: colorCyan},
		{name: "302 redirect", statusCode: 302, want: colorCyan},
		{name: "2xx success", statusCode: 200, want: colorGreen},
		{name: "below 2xx", statusCode: 100, want: colorGreen},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := requestStatusStyle(testCase.statusCode).GetForeground()
			if got != testCase.want {
				t.Errorf("requestStatusStyle(%d) foreground = %v, want %v", testCase.statusCode, got, testCase.want)
			}
		})
	}
}

// A redirect must never render with the same color as a client error — that is
// the exact regression (3xx mistaken for 4xx) the redirect arm exists to
// prevent.
func TestRequestStatusStyleRedirectDistinctFromClientError(t *testing.T) {
	redirect := requestStatusStyle(301).GetForeground()
	clientError := requestStatusStyle(404).GetForeground()
	if redirect == clientError {
		t.Errorf("3xx and 4xx share foreground %v; redirects must be visually distinct from errors", redirect)
	}
}
