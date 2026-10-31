package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

func colorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return isTerminal(w)
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func colorDiff(s string, enabled bool) string {
	if !enabled {
		return s
	}
	lines := strings.Split(s, "\n")
	inDiff := false
	for i, line := range lines {
		if strings.HasPrefix(line, "--- notion/") || strings.HasPrefix(line, "--- local/") {
			inDiff = true
		}
		if !inDiff {
			continue
		}
		color := ""
		switch {
		case strings.HasPrefix(line, "--- notion/"), strings.HasPrefix(line, "+++ local/"), strings.HasPrefix(line, "--- local/"), strings.HasPrefix(line, "+++ notion/"):
			color = "1"
		case strings.HasPrefix(line, "@@ "):
			color = "36"
		case strings.HasPrefix(line, "+"):
			color = "38;2;0;80;34;48;2;225;245;228"
		case strings.HasPrefix(line, "-"):
			color = "38;2;110;25;25;48;2;255;226;226"
		}
		if color != "" {
			lines[i] = "\x1b[" + color + "m" + line + "\x1b[0m"
		}
	}
	return strings.Join(lines, "\n")
}

func syncMessage(terminal bool, format string, args ...any) string {
	message := fmt.Sprintf(format, args...)
	if terminal && format == "%s" && len(args) == 1 {
		if d, ok := args[0].(interface{ NumberedDiff() string }); ok {
			message = d.NumberedDiff()
		}
	}
	return message
}

func printSync(cmdOut io.Writer, format string, args ...any) {
	message := syncMessage(isTerminal(cmdOut), format, args...)
	_, _ = fmt.Fprintln(cmdOut, colorDiff(message, colorEnabled(cmdOut)))
}

// FormatError adds line numbers and colors to a conflict diff on a terminal.
func FormatError(err error, w io.Writer) string {
	return formatError(err, isTerminal(w), colorEnabled(w))
}

func formatError(err error, terminal, color bool) string {
	message := err.Error()
	if terminal {
		var view interface{ DiffViews() (string, string) }
		if errors.As(err, &view) {
			plain, numbered := view.DiffViews()
			if plain != "" {
				message = strings.Replace(message, plain, numbered, 1)
			}
		}
	}
	return colorDiff(message, color)
}
