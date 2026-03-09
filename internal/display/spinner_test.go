package display

import (
	"bytes"
	"strings"
	"testing"
)

func TestSpinnerNonTTYFallback(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	defer SetOutput(nil)

	spin := NewSpinner("Connecting...")
	spin.Start()
	spin.Stop()

	result := buf.String()
	if !strings.Contains(result, "Connecting...") {
		t.Errorf("expected non-TTY fallback to print message, got %q", result)
	}
}

func TestSpinnerUpdate(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	defer SetOutput(nil)

	spin := NewSpinner("initial message")
	spin.Update("updated message")

	spin.mu.Lock()
	msg := spin.message
	spin.mu.Unlock()

	if msg != "updated message" {
		t.Errorf("expected message to be 'updated message', got %q", msg)
	}

	// Start and stop to ensure no panic in non-TTY mode
	spin.Start()
	spin.Stop()
}

func TestSpinnerDoubleStop(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	defer SetOutput(nil)

	spin := NewSpinner("test")
	spin.Start()
	spin.Stop()
	spin.Stop() // should not panic
}
