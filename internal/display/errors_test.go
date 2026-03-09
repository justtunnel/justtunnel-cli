package display

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestPrintErrorNetwork(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	defer SetOutput(nil)

	PrintError(NetworkError("could not reach server"))

	result := buf.String()
	if !strings.Contains(result, "Connection error") {
		t.Errorf("expected 'Connection error' prefix, got %q", result)
	}
	if !strings.Contains(result, "could not reach server") {
		t.Errorf("expected error message, got %q", result)
	}
	if !strings.Contains(result, "Check your internet connection") {
		t.Errorf("expected suggestion, got %q", result)
	}
}

func TestPrintErrorAuth(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	defer SetOutput(nil)

	PrintError(AuthError("invalid API key"))

	result := buf.String()
	if !strings.Contains(result, "Auth error") {
		t.Errorf("expected 'Auth error' prefix, got %q", result)
	}
	if !strings.Contains(result, "invalid API key") {
		t.Errorf("expected error message, got %q", result)
	}
	if !strings.Contains(result, "justtunnel auth") {
		t.Errorf("expected suggestion, got %q", result)
	}
}

func TestPrintErrorInput(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	defer SetOutput(nil)

	PrintError(InputError("invalid port: abc"))

	result := buf.String()
	if !strings.Contains(result, "Error") {
		t.Errorf("expected 'Error' prefix, got %q", result)
	}
	if !strings.Contains(result, "invalid port: abc") {
		t.Errorf("expected error message, got %q", result)
	}
}

func TestPrintErrorServer(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	defer SetOutput(nil)

	PrintError(ServerError("server error (HTTP 500)"))

	result := buf.String()
	if !strings.Contains(result, "Server error") {
		t.Errorf("expected 'Server error' prefix, got %q", result)
	}
	if !strings.Contains(result, "server error (HTTP 500)") {
		t.Errorf("expected error message, got %q", result)
	}
	if !strings.Contains(result, "status.justtunnel.dev") {
		t.Errorf("expected suggestion, got %q", result)
	}
}

func TestPrintErrorPlain(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	defer SetOutput(nil)

	PrintError(errors.New("something went wrong"))

	result := buf.String()
	if !strings.Contains(result, "Error:") {
		t.Errorf("expected 'Error:' prefix, got %q", result)
	}
	if !strings.Contains(result, "something went wrong") {
		t.Errorf("expected error message, got %q", result)
	}
}

func TestCLIErrorInterface(t *testing.T) {
	err := NetworkError("test error")
	if err.Error() != "test error" {
		t.Errorf("expected Error() to return message, got %q", err.Error())
	}
}
