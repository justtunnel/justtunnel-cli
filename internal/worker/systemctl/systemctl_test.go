package systemctl

import "testing"

func TestUnitName(t *testing.T) {
	got := UnitName("alpha")
	want := "justtunnel-worker-alpha.service"
	if got != want {
		t.Fatalf("UnitName = %q, want %q", got, want)
	}
}

func TestParseIsActive(t *testing.T) {
	cases := []struct {
		label      string
		output     string
		exitCode   int
		wantState  ProbeState
		wantDetail string
	}{
		{"active exit 0", "active\n", 0, ProbeStateRunning, ""},
		{"inactive exit 3", "inactive\n", 3, ProbeStateWaiting, ""},
		{"failed exit 3", "failed\n", 3, ProbeStateWaiting, "failed"},
		{"unknown exit 4", "unknown\n", 4, ProbeStateNotLoaded, ""},
		{"active no newline", "active", 0, ProbeStateRunning, ""},
		{"empty exit 0 falls back to exit code", "", 0, ProbeStateRunning, ""},
		{"empty exit 3 falls back to exit code", "", 3, ProbeStateWaiting, ""},
		{"empty exit 4 falls back to exit code", "", 4, ProbeStateNotLoaded, ""},
		{"activating exit 3", "activating\n", 3, ProbeStateWaiting, "activating"},
		{"deactivating exit 3", "deactivating\n", 3, ProbeStateWaiting, "deactivating"},
		{"reloading is unrecognized", "reloading\n", 1, ProbeStateUnknown, "reloading"},
		{"empty empty", "", 0, ProbeStateRunning, ""},
		{"upper case active", "ACTIVE\n", 0, ProbeStateRunning, ""},
		{"trailing whitespace failed", "  failed  \n", 3, ProbeStateWaiting, "failed"},
	}
	for _, testCase := range cases {
		t.Run(testCase.label, func(subTest *testing.T) {
			gotState, gotDetail := ParseIsActive(testCase.output, testCase.exitCode)
			if gotState != testCase.wantState {
				subTest.Fatalf("state = %v, want %v", gotState, testCase.wantState)
			}
			if gotDetail != testCase.wantDetail {
				subTest.Fatalf("detail = %q, want %q", gotDetail, testCase.wantDetail)
			}
		})
	}
}

func TestParseLingerEnabled(t *testing.T) {
	cases := []struct {
		label  string
		output string
		want   bool
	}{
		{"enabled", "Linger=yes\n", true},
		{"disabled", "Linger=no\n", false},
		{"empty", "", false},
		{"missing key", "Foo=yes\n", false},
		{"upper YES", "Linger=YES\n", true},
		{"trailing whitespace", "Linger=yes  \n", true},
		{"multiple props", "Other=1\nLinger=yes\nMore=2\n", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.label, func(subTest *testing.T) {
			if got := ParseLingerEnabled(testCase.output); got != testCase.want {
				subTest.Fatalf("got %v, want %v", got, testCase.want)
			}
		})
	}
}
