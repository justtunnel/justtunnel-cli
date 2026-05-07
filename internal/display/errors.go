package display

import (
	"errors"
	"fmt"
)

type ErrorCategory int

const (
	CategoryNetwork ErrorCategory = iota
	CategoryAuth
	CategoryInput
	CategoryServer
	// CategoryForbidden is for 403 / policy-rejection style errors where
	// the user IS authenticated but the requested action is not allowed
	// (plan limits, missing service token, suspended team, etc.). It
	// renders without the misleading "re-authenticate" suggestion that
	// CategoryAuth carries. See justtunnel-cli#47.
	CategoryForbidden
)

type CLIError struct {
	Category   ErrorCategory
	Message    string
	Suggestion string
}

func (cliErr *CLIError) Error() string {
	return cliErr.Message
}

func NetworkError(message string) *CLIError {
	return &CLIError{
		Category:   CategoryNetwork,
		Message:    message,
		Suggestion: "Check your internet connection and try again.",
	}
}

func AuthError(message string) *CLIError {
	return &CLIError{
		Category:   CategoryAuth,
		Message:    message,
		Suggestion: "Run `justtunnel auth` to re-authenticate.",
	}
}

func InputError(message string) *CLIError {
	return &CLIError{
		Category:   CategoryInput,
		Message:    message,
	}
}

func ServerError(message string) *CLIError {
	return &CLIError{
		Category:   CategoryServer,
		Message:    message,
		Suggestion: "Try again, or check https://status.justtunnel.dev",
	}
}

// ForbiddenError builds a 403-style CLIError. Suggestion is caller-supplied
// because the right next step depends on context (plan upgrade, install a
// service token, contact billing, etc.) — anything generic risks
// reintroducing the "re-authenticate" misdirection that #47 fixed.
func ForbiddenError(message, suggestion string) *CLIError {
	return &CLIError{
		Category:   CategoryForbidden,
		Message:    message,
		Suggestion: suggestion,
	}
}

func PrintError(err error) {
	var cliErr *CLIError
	if !errors.As(err, &cliErr) {
		colorRed.Fprintf(output, "\n  Error: ")
		fmt.Fprintf(output, "%s\n\n", err.Error())
		return
	}

	var prefix string
	prefixColor := colorRed
	switch cliErr.Category {
	case CategoryNetwork:
		prefix = "Connection error"
	case CategoryAuth:
		prefix = "Auth error"
		prefixColor = colorYellow
	case CategoryInput:
		prefix = "Error"
	case CategoryServer:
		prefix = "Server error"
	case CategoryForbidden:
		prefix = "Forbidden"
		prefixColor = colorYellow
	}

	fmt.Fprintln(output)
	prefixColor.Fprintf(output, "  %s: ", prefix)
	fmt.Fprintf(output, "%s\n", cliErr.Message)

	if cliErr.Suggestion != "" {
		fmt.Fprintln(output)
		colorDim.Fprintf(output, "  %s\n", cliErr.Suggestion)
	}
	fmt.Fprintln(output)
}
