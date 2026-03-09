package display

import (
	"os"
	"strings"

	"github.com/fatih/color"
	"golang.org/x/term"
)

var (
	colorCyan   = color.New(color.FgCyan)
	colorWhite  = color.New(color.FgWhite, color.Bold)
	colorGreen  = color.New(color.FgGreen)
	colorYellow = color.New(color.FgYellow)
	colorRed    = color.New(color.FgRed)
	colorDim    = color.New(color.Faint)
	colorBold   = color.New(color.Bold)
)

// IsTerminal reports whether the output writer is a terminal.
func IsTerminal() bool {
	file, ok := output.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

// TerminalWidth returns the width of the output terminal, or 0 if not a terminal.
func TerminalWidth() int {
	file, ok := output.(*os.File)
	if !ok {
		return 0
	}
	width, _, err := term.GetSize(int(file.Fd()))
	if err != nil {
		return 0
	}
	return width
}

// Bold writes text in bold to the given writer.
func Bold(text string) string {
	return colorBold.Sprint(text)
}

// sanitize strips terminal control characters from server-controlled strings
// to prevent ANSI injection attacks.
func sanitize(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))
	for _, char := range text {
		if char == '\033' || (char < 0x20 && char != '\t') {
			continue
		}
		builder.WriteRune(char)
	}
	return builder.String()
}
