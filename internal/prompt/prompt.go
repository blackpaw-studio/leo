package prompt

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
)

var (
	Bold    = color.New(color.Bold)
	Success = color.New(color.FgGreen, color.Bold)
	Warn    = color.New(color.FgYellow, color.Bold)
	Err     = color.New(color.FgRed, color.Bold)
	Info    = color.New(color.FgCyan)
)

// Prompt asks the user for input with an optional default value.
func Prompt(reader *bufio.Reader, label, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("%s [%s]: ", label, defaultVal)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal
	}
	return line
}

// PromptInt asks the user for an integer with a default value.
func PromptInt(reader *bufio.Reader, label string, defaultVal int) int {
	s := Prompt(reader, label, "")
	if s == "" {
		return defaultVal
	}
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

// YesNo asks a yes/no question with a default.
func YesNo(reader *bufio.Reader, label string, defaultYes bool) bool {
	suffix := " [Y/n]: "
	if !defaultYes {
		suffix = " [y/N]: "
	}
	fmt.Print(label + suffix)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "" {
		return defaultYes
	}
	return line == "y" || line == "yes"
}

// ParseChoice parses a numeric choice string, returning a 1-based index clamped to [1, max].
func ParseChoice(s string, max int) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	if n < 1 || n > max {
		return 1
	}
	return n
}

// ExpandHome replaces a leading ~/ with the user's home directory.
func ExpandHome(path string) string {
	if len(path) > 1 && path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// NewReader creates a new bufio.Reader from stdin.
func NewReader() *bufio.Reader {
	return bufio.NewReader(os.Stdin)
}
