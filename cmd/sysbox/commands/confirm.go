package commands

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// confirmPrompt asks the user to type "yes" to proceed.
// Returns (true, nil) if confirmed, (false, nil) if denied.
// When stdin is not a terminal (piped/scripted), returns an error
// so the caller can require --auto-approve instead.
func confirmPrompt(action string) (bool, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, fmt.Errorf("stdin is not a terminal; pass --auto-approve to skip confirmation")
	}
	fmt.Printf("\n%s? Only 'yes' will be accepted: ", action)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return false, err
		}
		// EOF without error: user denied (e.g. Ctrl-D).
		return false, nil
	}
	return strings.TrimSpace(scanner.Text()) == "yes", nil
}
