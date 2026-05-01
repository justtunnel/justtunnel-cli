package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/justtunnel/justtunnel-cli/internal/version"
)

// TestRootCmd_VersionFlag_PrintsVersion verifies that the top-level --version
// flag prints the same three-line format produced by `justtunnel version`.
// This is wired by setting rootCmd.Version and overriding the version template
// via rootCmd.SetVersionTemplate.
func TestRootCmd_VersionFlag_PrintsVersion(t *testing.T) {
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"--version"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute with --version returned error: %v", err)
	}

	output := buf.String()

	expected := "justtunnel " + version.Version + "\n" +
		"  commit: " + version.Commit + "\n" +
		"  built:  " + version.Date + "\n"

	if output != expected {
		t.Errorf("--version output mismatch\nwant: %q\ngot:  %q", expected, output)
	}
}

// TestRootCmd_HelpOutput_IncludesVersion verifies that running --help shows
// the version somewhere in the output (cobra prepends a Version line when
// rootCmd.Version is set).
func TestRootCmd_HelpOutput_IncludesVersion(t *testing.T) {
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"--help"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute with --help returned error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, version.Version) {
		t.Errorf("expected --help output to contain version %q somewhere, got %q", version.Version, output)
	}
}
