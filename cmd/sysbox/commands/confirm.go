package commands

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// confirmPrompt asks the user to type "yes" to proceed.
// Returns (true, nil) if confirmed, (false, nil) if denied.
// Pass --auto-approve to bypass this.
func confirmPrompt(action string) (bool, error) {
	fmt.Printf("\n%s? Only 'yes' will be accepted: ", action)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return false, scanner.Err()
	}
	return strings.TrimSpace(scanner.Text()) == "yes", nil
}
