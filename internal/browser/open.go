package browser

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
)

// Open opens the specified URL in the default browser.
func Open(targetURL string) error {
	// Validate the URL to prevent command injection (especially on Windows
	// where cmd /c start passes the URL through the shell).
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme: %s", parsed.Scheme)
	}

	var cmd string
	var args []string

	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{targetURL}
	case "linux":
		cmd = "xdg-open"
		args = []string{targetURL}
	case "windows":
		// Use url.exe instead of cmd /c start to avoid shell metacharacter injection
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", targetURL}
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}

	return exec.Command(cmd, args...).Start()
}
