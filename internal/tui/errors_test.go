package tui

import (
	"net/http"
	"strings"
	"testing"
)

func TestParseServerError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		statusCode     int
		body           string
		wantCode       string
		wantMessage    string
		wantUpgradeURL string
		wantErr        bool
	}{
		{
			name:       "TUNNEL_LIMIT_REACHED with upgrade URL",
			statusCode: http.StatusForbidden,
			body: `{
				"error": "Free plan limited to 1 tunnel. Upgrade your plan for more.",
				"code": "TUNNEL_LIMIT_REACHED",
				"upgrade_url": "https://justtunnel.dev/pricing"
			}`,
			wantCode:       "TUNNEL_LIMIT_REACHED",
			wantMessage:    "Free plan limited to 1 tunnel. Upgrade your plan for more.",
			wantUpgradeURL: "https://justtunnel.dev/pricing",
			wantErr:        false,
		},
		{
			name:       "RESERVED_SUBDOMAIN_NOT_ALLOWED",
			statusCode: http.StatusForbidden,
			body: `{
				"error": "Subdomain 'myapp' is reserved by another account.",
				"code": "RESERVED_SUBDOMAIN_NOT_ALLOWED"
			}`,
			wantCode:       "RESERVED_SUBDOMAIN_NOT_ALLOWED",
			wantMessage:    "Subdomain 'myapp' is reserved by another account.",
			wantUpgradeURL: "",
			wantErr:        false,
		},
		{
			name:       "non-JSON 403 response falls back to generic error",
			statusCode: http.StatusForbidden,
			body:       "Forbidden",
			wantCode:       "",
			wantMessage:    "Forbidden",
			wantUpgradeURL: "",
			wantErr:        false,
		},
		{
			name:       "401 unauthorized response",
			statusCode: http.StatusUnauthorized,
			body: `{
				"error": "Invalid or expired API key"
			}`,
			wantCode:       "",
			wantMessage:    "Invalid or expired API key",
			wantUpgradeURL: "",
			wantErr:        false,
		},
		{
			name:       "401 non-JSON response",
			statusCode: http.StatusUnauthorized,
			body:       "Unauthorized",
			wantCode:       "",
			wantMessage:    "Unauthorized",
			wantUpgradeURL: "",
			wantErr:        false,
		},
		{
			name:       "empty body 403",
			statusCode: http.StatusForbidden,
			body:       "",
			wantCode:       "",
			wantMessage:    "server returned 403",
			wantUpgradeURL: "",
			wantErr:        false,
		},
		{
			name:       "empty body 401",
			statusCode: http.StatusUnauthorized,
			body:       "",
			wantCode:       "",
			wantMessage:    "server returned 401",
			wantUpgradeURL: "",
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			serverErr := ParseServerError(tt.statusCode, []byte(tt.body))

			if tt.wantErr {
				if serverErr != nil {
					t.Fatal("expected nil ServerError on error case")
				}
				return
			}

			if serverErr == nil {
				t.Fatal("expected non-nil ServerError")
			}

			if serverErr.Code != tt.wantCode {
				t.Errorf("Code = %q, want %q", serverErr.Code, tt.wantCode)
			}
			if serverErr.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", serverErr.Message, tt.wantMessage)
			}
			if serverErr.UpgradeURL != tt.wantUpgradeURL {
				t.Errorf("UpgradeURL = %q, want %q", serverErr.UpgradeURL, tt.wantUpgradeURL)
			}
		})
	}
}

func TestServerError_Error(t *testing.T) {
	t.Parallel()

	t.Run("error message includes code and message", func(t *testing.T) {
		t.Parallel()
		serverErr := &ServerError{
			Code:    "TUNNEL_LIMIT_REACHED",
			Message: "Free plan limited to 1 tunnel.",
		}

		errStr := serverErr.Error()
		if !strings.Contains(errStr, "Free plan limited to 1 tunnel.") {
			t.Errorf("Error() = %q, should contain message", errStr)
		}
	})

	t.Run("error message includes upgrade URL when present", func(t *testing.T) {
		t.Parallel()
		serverErr := &ServerError{
			Code:       "TUNNEL_LIMIT_REACHED",
			Message:    "Free plan limited to 1 tunnel.",
			UpgradeURL: "https://justtunnel.dev/pricing",
		}

		errStr := serverErr.Error()
		if !strings.Contains(errStr, "https://justtunnel.dev/pricing") {
			t.Errorf("Error() = %q, should contain upgrade URL", errStr)
		}
	})

	t.Run("error message without upgrade URL omits upgrade hint", func(t *testing.T) {
		t.Parallel()
		serverErr := &ServerError{
			Code:    "RESERVED_SUBDOMAIN_NOT_ALLOWED",
			Message: "Subdomain reserved.",
		}

		errStr := serverErr.Error()
		if strings.Contains(errStr, "Upgrade") {
			t.Errorf("Error() = %q, should not contain Upgrade hint", errStr)
		}
	})
}

func TestServerError_IsPlanLimit(t *testing.T) {
	t.Parallel()

	t.Run("TUNNEL_LIMIT_REACHED is plan limit", func(t *testing.T) {
		t.Parallel()
		serverErr := &ServerError{Code: "TUNNEL_LIMIT_REACHED"}
		if !serverErr.IsPlanLimit() {
			t.Error("expected IsPlanLimit() = true for TUNNEL_LIMIT_REACHED")
		}
	})

	t.Run("other codes are not plan limit", func(t *testing.T) {
		t.Parallel()
		serverErr := &ServerError{Code: "RESERVED_SUBDOMAIN_NOT_ALLOWED"}
		if serverErr.IsPlanLimit() {
			t.Error("expected IsPlanLimit() = false for RESERVED_SUBDOMAIN_NOT_ALLOWED")
		}
	})

	t.Run("empty code is not plan limit", func(t *testing.T) {
		t.Parallel()
		serverErr := &ServerError{Code: ""}
		if serverErr.IsPlanLimit() {
			t.Error("expected IsPlanLimit() = false for empty code")
		}
	})
}
