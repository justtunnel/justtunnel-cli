package launchctl

import "testing"

func TestLabel(t *testing.T) {
	if got := Label("alpha"); got != "dev.justtunnel.worker.alpha" {
		t.Fatalf("Label = %q", got)
	}
}

func TestParsePrint(t *testing.T) {
	cases := []struct {
		name       string
		output     string
		exitCode   int
		wantState  ProbeState
		wantDetail string
	}{
		{
			name:       "running with pid",
			output:     "dev.justtunnel.worker.alpha = {\n\tstate = running\n\tpid = 12345\n}",
			exitCode:   0,
			wantState:  ProbeStateRunning,
			wantDetail: "pid=12345",
		},
		{
			name:       "running no pid",
			output:     "  state = running\n",
			exitCode:   0,
			wantState:  ProbeStateRunning,
			wantDetail: "",
		},
		{
			name:       "waiting (KeepAlive between restarts)",
			output:     "  state = waiting\n  reason = KeepAlive\n",
			exitCode:   0,
			wantState:  ProbeStateWaiting,
			wantDetail: "",
		},
		{
			name:       "not loaded by exit code",
			output:     "",
			exitCode:   ExitCodeNotFound,
			wantState:  ProbeStateNotLoaded,
			wantDetail: "",
		},
		{
			name:       "not loaded by text",
			output:     "Could not find service \"dev.justtunnel.worker.alpha\" in domain for port",
			exitCode:   1,
			wantState:  ProbeStateNotLoaded,
			wantDetail: "",
		},
		{
			name:       "unknown other-state value",
			output:     "  state = spawn scheduled\n",
			exitCode:   0,
			wantState:  ProbeStateWaiting,
			wantDetail: "state=spawn scheduled",
		},
		{
			name:       "unknown empty stdout exit 0",
			output:     "",
			exitCode:   0,
			wantState:  ProbeStateUnknown,
			wantDetail: "",
		},
		{
			name:       "non-zero exit, not 113, not match",
			output:     "permission denied\n",
			exitCode:   1,
			wantState:  ProbeStateUnknown,
			wantDetail: "permission denied",
		},
		{
			name:       "trailing semicolon variant",
			output:     "\tstate = running;\n",
			exitCode:   0,
			wantState:  ProbeStateRunning,
			wantDetail: "",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(subTest *testing.T) {
			gotState, gotDetail := ParsePrint(testCase.output, testCase.exitCode)
			if gotState != testCase.wantState {
				subTest.Fatalf("state = %v, want %v", gotState, testCase.wantState)
			}
			if gotDetail != testCase.wantDetail {
				subTest.Fatalf("detail = %q, want %q", gotDetail, testCase.wantDetail)
			}
		})
	}
}
