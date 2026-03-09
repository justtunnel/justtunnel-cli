package display

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintBannerNonTTY(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	defer SetOutput(nil) // reset

	PrintBanner("myapp", "https://myapp.justtunnel.dev", "http://localhost:3000")

	result := buf.String()

	if !strings.Contains(result, "justtunnel") {
		t.Errorf("expected banner to contain 'justtunnel', got %q", result)
	}
	if !strings.Contains(result, "Forwarding:") {
		t.Errorf("expected banner to contain 'Forwarding:', got %q", result)
	}
	if !strings.Contains(result, "https://myapp.justtunnel.dev") {
		t.Errorf("expected banner to contain URL, got %q", result)
	}
	if !strings.Contains(result, "http://localhost:3000") {
		t.Errorf("expected banner to contain local target, got %q", result)
	}
	if !strings.Contains(result, "Subdomain:") {
		t.Errorf("expected banner to contain 'Subdomain:', got %q", result)
	}
	if !strings.Contains(result, "myapp") {
		t.Errorf("expected banner to contain subdomain, got %q", result)
	}
}
