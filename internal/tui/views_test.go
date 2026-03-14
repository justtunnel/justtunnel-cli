package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRenderListViewNarrowTerminal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		terminalWidth  int
		wantTruncated  bool
		wantMaxURLLen  int
	}{
		{
			name:           "width 50 truncates URLs to fit",
			terminalWidth:  50,
			wantTruncated:  true,
			wantMaxURLLen:  20,
		},
		{
			name:           "width 40 truncates URLs aggressively",
			terminalWidth:  40,
			wantTruncated:  true,
			wantMaxURLLen:  15,
		},
		{
			name:           "width 100 shows full URLs",
			terminalWidth:  100,
			wantTruncated:  false,
			wantMaxURLLen:  35,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model := newTestModel(t, 2)
			model.width = tt.terminalWidth
			model.height = 24

			output := renderListView(model)

			// For narrow terminals, the long URLs should be truncated (contain "...")
			if tt.wantTruncated {
				fullURL := "https://sub-a.justtunnel.io"
				if strings.Contains(output, fullURL) {
					t.Errorf("narrow terminal (width=%d) should truncate URL %q, but it appeared in full", tt.terminalWidth, fullURL)
				}
			} else {
				// Wide terminal should show the full URL
				if !strings.Contains(output, "https://sub-a.justtunnel.io") {
					t.Errorf("wide terminal (width=%d) should show full URL", tt.terminalWidth)
				}
			}
		})
	}
}

func TestRenderListViewShortTerminal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		terminalHeight int
		wantInputBar  bool
	}{
		{
			name:           "height 8 hides input bar",
			terminalHeight: 8,
			wantInputBar:   false,
		},
		{
			name:           "height 6 hides input bar",
			terminalHeight: 6,
			wantInputBar:   false,
		},
		{
			name:           "height 24 shows input bar",
			terminalHeight: 24,
			wantInputBar:   true,
		},
		{
			name:           "height 10 shows input bar",
			terminalHeight: 10,
			wantInputBar:   true,
		},
		{
			name:           "height 9 hides input bar",
			terminalHeight: 9,
			wantInputBar:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model := newTestModel(t, 2)
			model.height = tt.terminalHeight
			model.width = 80

			output := renderListView(model)

			// The input bar contains "> " prompt from renderInputBar
			hasInputBar := strings.Contains(output, "> ")
			if tt.wantInputBar && !hasInputBar {
				t.Errorf("height=%d should show input bar, but '> ' not found in output", tt.terminalHeight)
			}
			if !tt.wantInputBar && hasInputBar {
				t.Errorf("height=%d should hide input bar, but '> ' was found in output", tt.terminalHeight)
			}
		})
	}
}

func TestRenderListViewWidthAdaptsURLColumn(t *testing.T) {
	t.Parallel()

	// Create a model with a tunnel that has a long URL
	model := NewModel([]TunnelDisplayEntry{
		{
			ID:        1,
			Name:      "frontend",
			Port:      3000,
			PublicURL: "https://abc123def456.justtunnel.dev",
			State:     StateConnected,
			Requests:  42,
		},
	}, PlanInfo{Name: "Pro", MaxTunnels: 5})

	t.Run("narrow terminal URL gets truncated with ellipsis", func(t *testing.T) {
		t.Parallel()
		narrowModel := model
		narrowModel.width = 50
		narrowModel.height = 24

		output := renderListView(narrowModel)

		// The full URL should NOT appear
		if strings.Contains(output, "https://abc123def456.justtunnel.dev") {
			t.Error("narrow terminal should not show the full URL")
		}
		// But there should be a truncated URL with "..."
		if !strings.Contains(output, "...") {
			t.Error("narrow terminal should show truncated URL with ellipsis")
		}
	})

	t.Run("wide terminal shows full URL", func(t *testing.T) {
		t.Parallel()
		wideModel := model
		wideModel.width = 120
		wideModel.height = 24

		output := renderListView(wideModel)

		if !strings.Contains(output, "https://abc123def456.justtunnel.dev") {
			t.Error("wide terminal should show the full URL")
		}
	})
}

func TestFormatNonTTYEvent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		event    NonTTYEvent
		wantLine string
	}{
		{
			name: "connected event",
			event: NonTTYEvent{
				TunnelName: "frontend",
				Port:       3000,
				EventType:  "connected",
				URL:        "https://abc123.justtunnel.dev",
			},
			wantLine: "[frontend:3000] connected https://abc123.justtunnel.dev",
		},
		{
			name: "request event with method path status and latency",
			event: NonTTYEvent{
				TunnelName: "api",
				Port:       8080,
				EventType:  "request",
				Method:     "GET",
				Path:       "/api/users",
				Status:     200,
				Latency:    12 * time.Millisecond,
			},
			wantLine: "[api:8080] GET /api/users 200 12ms",
		},
		{
			name: "request event with POST",
			event: NonTTYEvent{
				TunnelName: "api",
				Port:       8080,
				EventType:  "request",
				Method:     "POST",
				Path:       "/login",
				Status:     201,
				Latency:    45 * time.Millisecond,
			},
			wantLine: "[api:8080] POST /login 201 45ms",
		},
		{
			name: "disconnected event",
			event: NonTTYEvent{
				TunnelName: "frontend",
				Port:       3000,
				EventType:  "disconnected",
			},
			wantLine: "[frontend:3000] disconnected",
		},
		{
			name: "reconnecting event",
			event: NonTTYEvent{
				TunnelName: "api",
				Port:       8080,
				EventType:  "reconnecting",
				Detail:     "attempt 3",
			},
			wantLine: "[api:8080] reconnecting attempt 3",
		},
		{
			name: "name defaults to port when empty",
			event: NonTTYEvent{
				TunnelName: "",
				Port:       3000,
				EventType:  "connected",
				URL:        "https://abc.justtunnel.dev",
			},
			wantLine: "[:3000] connected https://abc.justtunnel.dev",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotLine := FormatNonTTYEvent(tt.event)

			if gotLine != tt.wantLine {
				t.Errorf("FormatNonTTYEvent() =\n  %q\nwant:\n  %q", gotLine, tt.wantLine)
			}
		})
	}
}

func TestURLColumnWidthForTerminal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		terminalWidth int
		wantMin       int
		wantMax       int
	}{
		{
			name:          "very narrow terminal gets small URL column",
			terminalWidth: 40,
			wantMin:       10,
			wantMax:       15,
		},
		{
			name:          "medium terminal gets moderate URL column",
			terminalWidth: 80,
			wantMin:       25,
			wantMax:       35,
		},
		{
			name:          "wide terminal gets full URL column",
			terminalWidth: 120,
			wantMin:       35,
			wantMax:       60,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			urlWidth := urlColumnWidth(tt.terminalWidth)

			if urlWidth < tt.wantMin {
				t.Errorf("urlColumnWidth(%d) = %d, want >= %d", tt.terminalWidth, urlWidth, tt.wantMin)
			}
			if urlWidth > tt.wantMax {
				t.Errorf("urlColumnWidth(%d) = %d, want <= %d", tt.terminalWidth, urlWidth, tt.wantMax)
			}
		})
	}
}

func TestShortTerminalHidesInputBarInDetailView(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, 2)
	model.viewState = viewDetail
	model.height = 8
	model.width = 80

	output := renderDetailView(model)

	// Input bar should be hidden when height < 10
	if strings.Contains(output, "> ") {
		t.Error("short terminal (height=8) should hide input bar in detail view")
	}
}

func TestSelectionMarkerVisibleOnNarrowTerminal(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, 3)
	model.width = 50
	model.height = 24

	output := renderListView(model)

	// Even on a narrow terminal, the selection marker should be present
	if !strings.Contains(output, ">") {
		t.Error("narrow terminal should still show selection marker")
	}
}

func TestTruncateString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "short string unchanged",
			input:  "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "exact length unchanged",
			input:  "hello",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "long string truncated with ellipsis",
			input:  "https://long-subdomain.justtunnel.dev",
			maxLen: 20,
			want:   "https://long-subd...",
		},
		{
			name:   "very small max length no ellipsis",
			input:  "abcdef",
			maxLen: 3,
			want:   "abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncateString(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateString(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestRenderListViewCombinedNarrowAndShort(t *testing.T) {
	t.Parallel()

	// Both narrow AND short: URLs truncated and input bar hidden
	model := newTestModel(t, 2)
	model.width = 45
	model.height = 7

	output := renderListView(model)

	// Should NOT contain full URLs
	fullURL := "https://sub-a.justtunnel.io"
	if strings.Contains(output, fullURL) {
		t.Error("narrow+short terminal should truncate URLs")
	}

	// Should NOT contain the input bar
	// The ">" could appear in the selection marker, so check for "> " (the input prompt)
	lines := strings.Split(output, "\n")
	lastNonEmpty := ""
	for lineIdx := len(lines) - 1; lineIdx >= 0; lineIdx-- {
		if strings.TrimSpace(lines[lineIdx]) != "" {
			lastNonEmpty = lines[lineIdx]
			break
		}
	}
	// The last non-empty line should NOT be the input prompt
	if strings.HasPrefix(strings.TrimSpace(lastNonEmpty), "> ") || lastNonEmpty == "> " {
		t.Error("narrow+short terminal should hide input bar")
	}

	// Should still contain header
	if !strings.Contains(output, "justtunnel") {
		t.Error("even narrow+short terminal should show header")
	}

	// Should still contain tunnel data
	if !strings.Contains(output, "3000") {
		t.Error("even narrow+short terminal should show port numbers")
	}
}

// Test that the format function returns empty prefix when name is empty
func TestFormatNonTTYEventNameFallback(t *testing.T) {
	t.Parallel()

	event := NonTTYEvent{
		TunnelName: "",
		Port:       3000,
		EventType:  "connected",
		URL:        "https://test.justtunnel.dev",
	}

	result := FormatNonTTYEvent(event)
	if !strings.HasPrefix(result, "[:3000]") {
		t.Errorf("expected prefix '[:3000]', got %q", result)
	}
}

// Test error event in non-TTY format
func TestFormatNonTTYEventError(t *testing.T) {
	t.Parallel()

	event := NonTTYEvent{
		TunnelName: "web",
		Port:       8080,
		EventType:  "error",
		Detail:     "connection refused",
	}

	result := FormatNonTTYEvent(event)
	want := "[web:8080] error connection refused"
	if result != want {
		t.Errorf("FormatNonTTYEvent() = %q, want %q", result, want)
	}
}

// Test inputBarVisible helper function
func TestInputBarVisible(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		height int
		want   bool
	}{
		{"height 5 hidden", 5, false},
		{"height 9 hidden", 9, false},
		{"height 10 visible", 10, true},
		{"height 24 visible", 24, true},
		{"height 0 hidden", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := inputBarVisible(tt.height)
			if got != tt.want {
				t.Errorf("inputBarVisible(%d) = %v, want %v", tt.height, got, tt.want)
			}
		})
	}
}

// Verify the format helper uses proper formatting similar to the spec examples
func TestFormatNonTTYEventMatchesSpec(t *testing.T) {
	t.Parallel()

	// These match the exact examples from the issue spec
	specExamples := []struct {
		event NonTTYEvent
		want  string
	}{
		{
			event: NonTTYEvent{TunnelName: "frontend", Port: 3000, EventType: "connected", URL: "https://abc123.justtunnel.dev"},
			want:  "[frontend:3000] connected https://abc123.justtunnel.dev",
		},
		{
			event: NonTTYEvent{TunnelName: "api", Port: 8080, EventType: "connected", URL: "https://def456.justtunnel.dev"},
			want:  "[api:8080] connected https://def456.justtunnel.dev",
		},
		{
			event: NonTTYEvent{TunnelName: "frontend", Port: 3000, EventType: "request", Method: "GET", Path: "/api/users", Status: 200, Latency: 12 * time.Millisecond},
			want:  "[frontend:3000] GET /api/users 200 12ms",
		},
		{
			event: NonTTYEvent{TunnelName: "api", Port: 8080, EventType: "request", Method: "POST", Path: "/login", Status: 201, Latency: 45 * time.Millisecond},
			want:  "[api:8080] POST /login 201 45ms",
		},
	}

	for idx, example := range specExamples {
		t.Run(fmt.Sprintf("spec_example_%d", idx), func(t *testing.T) {
			t.Parallel()
			got := FormatNonTTYEvent(example.event)
			if got != example.want {
				t.Errorf("FormatNonTTYEvent() =\n  %q\nwant:\n  %q", got, example.want)
			}
		})
	}
}
