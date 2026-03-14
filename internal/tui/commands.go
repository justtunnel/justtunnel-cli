package tui

import (
	"fmt"
	"strconv"
	"strings"
)

// Command is the interface for all parsed slash commands.
// The parser returns a Command value (or error) without executing any side effects.
type Command interface {
	commandType() string
}

// AddCommand represents a parsed /add command with a validated port
// and optional name/subdomain flags.
type AddCommand struct {
	Port      int
	Name      string
	Subdomain string
}

func (AddCommand) commandType() string { return "add" }

// RemoveCommand represents a parsed /remove (or /stop) command.
// Target is the raw string argument — it could be an index or port number.
// Resolution happens at the TUI model layer, not in the parser.
type RemoveCommand struct {
	Target string
}

func (RemoveCommand) commandType() string { return "remove" }

// ListCommand represents a parsed /list command.
type ListCommand struct{}

func (ListCommand) commandType() string { return "list" }

// QuitCommand represents a parsed /quit command.
type QuitCommand struct{}

func (QuitCommand) commandType() string { return "quit" }

// HelpCommand represents a parsed /help command.
type HelpCommand struct{}

func (HelpCommand) commandType() string { return "help" }

// ParseCommand parses a raw input string into a typed Command.
// Returns (nil, nil) if the input is empty or doesn't start with "/".
// Returns (nil, error) if the command is malformed or unknown.
// Returns (Command, nil) on success.
func ParseCommand(input string) (Command, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" || !strings.HasPrefix(trimmed, "/") {
		return nil, nil
	}

	tokens := strings.Fields(trimmed)
	cmdName := strings.ToLower(tokens[0])
	args := tokens[1:]

	switch cmdName {
	case "/add":
		return parseAddCommand(args)
	case "/remove", "/stop":
		return parseRemoveCommand(args)
	case "/list":
		return ListCommand{}, nil
	case "/quit":
		return QuitCommand{}, nil
	case "/help":
		return HelpCommand{}, nil
	default:
		// Use tokens[0] (not cmdName) to preserve the user's original casing in the error
		return nil, fmt.Errorf("Unknown command: %s. Type /help for available commands.", tokens[0])
	}
}

// parseAddCommand parses the arguments for /add: <port> [--name <name>] [--subdomain <subdomain>].
func parseAddCommand(args []string) (Command, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("Usage: /add <port> [--name <name>] [--subdomain <subdomain>]")
	}

	portStr := args[0]
	port, parseErr := strconv.Atoi(portStr)
	if parseErr != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("Invalid port: %s (must be 1-65535).", portStr)
	}

	cmd := AddCommand{Port: port}

	// Parse optional flags from remaining args
	flagArgs := args[1:]
	for idx := 0; idx < len(flagArgs); idx++ {
		switch flagArgs[idx] {
		case "--name":
			if idx+1 >= len(flagArgs) {
				return nil, fmt.Errorf("Usage: /add <port> [--name <name>] [--subdomain <subdomain>]")
			}
			idx++
			cmd.Name = flagArgs[idx]
		case "--subdomain":
			if idx+1 >= len(flagArgs) {
				return nil, fmt.Errorf("Usage: /add <port> [--name <name>] [--subdomain <subdomain>]")
			}
			idx++
			cmd.Subdomain = flagArgs[idx]
		}
	}

	return cmd, nil
}

// parseRemoveCommand parses the arguments for /remove (or /stop): <index|port>.
func parseRemoveCommand(args []string) (Command, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("Usage: /remove <index|port>")
	}

	return RemoveCommand{Target: args[0]}, nil
}
