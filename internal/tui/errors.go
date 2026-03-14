package tui

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ServerError represents a structured error response from the tunnel server.
// The server returns these as JSON bodies on HTTP 401/403 responses before
// the WebSocket upgrade handshake.
type ServerError struct {
	// Code is the machine-readable error code (e.g., "TUNNEL_LIMIT_REACHED").
	Code string `json:"code,omitempty"`
	// Message is the human-readable error description.
	Message string `json:"error"`
	// UpgradeURL is an optional URL where the user can upgrade their plan.
	UpgradeURL string `json:"upgrade_url,omitempty"`
}

// Error implements the error interface, producing a user-friendly message
// that includes the upgrade URL when present.
func (e *ServerError) Error() string {
	if e.UpgradeURL != "" {
		return fmt.Sprintf("%s\nUpgrade your plan: %s", e.Message, e.UpgradeURL)
	}
	return e.Message
}

// IsPlanLimit returns true if this error indicates the user has hit their
// plan's tunnel limit and needs to upgrade.
func (e *ServerError) IsPlanLimit() bool {
	return e.Code == "TUNNEL_LIMIT_REACHED"
}

// ParseServerError parses a server error response body into a structured
// ServerError. It handles both JSON and plain-text responses gracefully.
// Returns a ServerError with whatever information could be extracted.
func ParseServerError(statusCode int, body []byte) *ServerError {
	trimmedBody := strings.TrimSpace(string(body))

	// Try to parse as JSON first
	if len(trimmedBody) > 0 {
		var parsed ServerError
		if err := json.Unmarshal([]byte(trimmedBody), &parsed); err == nil && parsed.Message != "" {
			return &parsed
		}

		// Non-JSON body: use it as the message directly
		return &ServerError{
			Message: trimmedBody,
		}
	}

	// Empty body: generate a generic message from the status code
	return &ServerError{
		Message: fmt.Sprintf("server returned %d", statusCode),
	}
}
