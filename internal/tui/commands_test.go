package tui

import (
	"testing"
)

func TestParseCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		wantCmd    Command
		wantErr    bool
		wantErrMsg string
	}{
		// Happy path: /add
		{
			name:    "add with port only",
			input:   "/add 8080",
			wantCmd: AddCommand{Port: 8080},
		},
		{
			name:    "add with port and name flag",
			input:   "/add 8080 --name api",
			wantCmd: AddCommand{Port: 8080, Name: "api"},
		},
		{
			name:    "add with port and subdomain flag",
			input:   "/add 8080 --subdomain my-api",
			wantCmd: AddCommand{Port: 8080, Subdomain: "my-api"},
		},
		{
			name:    "add with all flags",
			input:   "/add 8080 --name api --subdomain my-api",
			wantCmd: AddCommand{Port: 8080, Name: "api", Subdomain: "my-api"},
		},
		{
			name:    "add with flags in reverse order",
			input:   "/add 8080 --subdomain my-api --name api",
			wantCmd: AddCommand{Port: 8080, Name: "api", Subdomain: "my-api"},
		},
		{
			name:    "add with minimum valid port",
			input:   "/add 1",
			wantCmd: AddCommand{Port: 1},
		},
		{
			name:    "add with maximum valid port",
			input:   "/add 65535",
			wantCmd: AddCommand{Port: 65535},
		},

		// Case-insensitive commands
		{
			name:    "add uppercase",
			input:   "/ADD 8080",
			wantCmd: AddCommand{Port: 8080},
		},
		{
			name:    "add mixed case",
			input:   "/Add 8080",
			wantCmd: AddCommand{Port: 8080},
		},

		// /remove
		{
			name:    "remove with index",
			input:   "/remove 1",
			wantCmd: RemoveCommand{Target: "1"},
		},
		{
			name:    "remove with port number",
			input:   "/remove 3000",
			wantCmd: RemoveCommand{Target: "3000"},
		},

		// /stop (alias for /remove)
		{
			name:    "stop alias for remove",
			input:   "/stop 3000",
			wantCmd: RemoveCommand{Target: "3000"},
		},

		// /list
		{
			name:    "list command",
			input:   "/list",
			wantCmd: ListCommand{},
		},

		// /quit
		{
			name:    "quit command",
			input:   "/quit",
			wantCmd: QuitCommand{},
		},

		// /help
		{
			name:    "help command",
			input:   "/help",
			wantCmd: HelpCommand{},
		},

		// Empty / no command
		{
			name:    "empty string returns nil",
			input:   "",
			wantCmd: nil,
		},
		{
			name:    "whitespace only returns nil",
			input:   "   ",
			wantCmd: nil,
		},
		{
			name:    "non-slash input returns nil",
			input:   "hello world",
			wantCmd: nil,
		},

		// Error cases
		{
			name:       "unknown command",
			input:      "/unknown",
			wantErr:    true,
			wantErrMsg: "Unknown command: /unknown. Type /help for available commands.",
		},
		{
			name:       "invalid port not a number",
			input:      "/add abc",
			wantErr:    true,
			wantErrMsg: "Invalid port: abc (must be 1-65535).",
		},
		{
			name:       "port zero out of range",
			input:      "/add 0",
			wantErr:    true,
			wantErrMsg: "Invalid port: 0 (must be 1-65535).",
		},
		{
			name:       "port too high",
			input:      "/add 99999",
			wantErr:    true,
			wantErrMsg: "Invalid port: 99999 (must be 1-65535).",
		},
		{
			name:       "negative port",
			input:      "/add -1",
			wantErr:    true,
			wantErrMsg: "Invalid port: -1 (must be 1-65535).",
		},
		{
			name:       "add missing port",
			input:      "/add",
			wantErr:    true,
			wantErrMsg: "Usage: /add <port> [--name <name>] [--subdomain <subdomain>]",
		},
		{
			name:       "remove missing target",
			input:      "/remove",
			wantErr:    true,
			wantErrMsg: "Usage: /remove <index|port>",
		},
		{
			name:       "stop missing target",
			input:      "/stop",
			wantErr:    true,
			wantErrMsg: "Usage: /remove <index|port>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cmd, err := ParseCommand(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if err.Error() != tt.wantErrMsg {
					t.Errorf("error message = %q, want %q", err.Error(), tt.wantErrMsg)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantCmd == nil {
				if cmd != nil {
					t.Errorf("expected nil command, got %#v", cmd)
				}
				return
			}

			if cmd == nil {
				t.Fatalf("expected command %#v, got nil", tt.wantCmd)
			}

			switch expected := tt.wantCmd.(type) {
			case AddCommand:
				got, ok := cmd.(AddCommand)
				if !ok {
					t.Fatalf("expected AddCommand, got %T", cmd)
				}
				if got.Port != expected.Port {
					t.Errorf("Port = %d, want %d", got.Port, expected.Port)
				}
				if got.Name != expected.Name {
					t.Errorf("Name = %q, want %q", got.Name, expected.Name)
				}
				if got.Subdomain != expected.Subdomain {
					t.Errorf("Subdomain = %q, want %q", got.Subdomain, expected.Subdomain)
				}

			case RemoveCommand:
				got, ok := cmd.(RemoveCommand)
				if !ok {
					t.Fatalf("expected RemoveCommand, got %T", cmd)
				}
				if got.Target != expected.Target {
					t.Errorf("Target = %q, want %q", got.Target, expected.Target)
				}

			case ListCommand:
				if _, ok := cmd.(ListCommand); !ok {
					t.Fatalf("expected ListCommand, got %T", cmd)
				}

			case QuitCommand:
				if _, ok := cmd.(QuitCommand); !ok {
					t.Fatalf("expected QuitCommand, got %T", cmd)
				}

			case HelpCommand:
				if _, ok := cmd.(HelpCommand); !ok {
					t.Fatalf("expected HelpCommand, got %T", cmd)
				}

			default:
				t.Fatalf("unhandled command type in test: %T", expected)
			}
		})
	}
}

func TestCommandType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cmd      Command
		wantType string
	}{
		{"add command type", AddCommand{Port: 8080}, "add"},
		{"remove command type", RemoveCommand{Target: "1"}, "remove"},
		{"list command type", ListCommand{}, "list"},
		{"quit command type", QuitCommand{}, "quit"},
		{"help command type", HelpCommand{}, "help"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.cmd.commandType(); got != tt.wantType {
				t.Errorf("commandType() = %q, want %q", got, tt.wantType)
			}
		})
	}
}
