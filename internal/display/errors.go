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
