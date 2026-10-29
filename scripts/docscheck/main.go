// Check the three maintained documents. Historical Markdown is stored only in
// the archive, so adding a second handbook fails this gate rather than drifting.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var expectedDocs = []string{"README.md", "docs/architecture.md", "docs/interfaces.md"}
var markdownLink = regexp.MustCompile(`\[[^]]*\]\(([^)]+)\)`)

func main() {
	// Use Git's ignore rules to exclude local lab outputs, vendor trees and tools.
	// Tracked deletions are ignored; new untracked documentation is still checked.
	raw, err := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard", "-z").Output()
	check(err)
	seen := map[string]bool{}
	var actual []string
	for _, path := range strings.Split(string(raw), "\x00") {
		if filepath.Ext(path) != ".md" || seen[path] {
			continue
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else {
			check(err)
		}
		seen[path] = true
		actual = append(actual, path)
	}
	sort.Strings(actual)
	sort.Strings(expectedDocs)
	if strings.Join(actual, "\n") != strings.Join(expectedDocs, "\n") {
		fail(fmt.Sprintf("only three maintained documents are allowed; got %v", actual))
	}
	for _, file := range actual {
		raw, err := os.ReadFile(file)
		check(err)
		for _, match := range markdownLink.FindAllStringSubmatch(string(raw), -1) {
			target := strings.SplitN(match[1], "#", 2)[0]
			if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(file), target))
			if _, err := os.Stat(resolved); err != nil {
				fail(fmt.Sprintf("%s has broken link %s", file, match[1]))
			}
		}
	}
	fmt.Printf("documentation checks passed (%d maintained docs)\n", len(actual))
}

func check(err error) {
	if err != nil {
		fail(err.Error())
	}
}
func fail(message string) { fmt.Fprintln(os.Stderr, "docs check:", message); os.Exit(1) }
