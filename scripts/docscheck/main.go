package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var expectedDocs = []string{
	"docs/architecture.md",
	"docs/design-principles.zh-CN.md",
	"docs/project-proposal.zh-CN.md",
	"docs/development/contributing.md",
	"docs/development/releasing.md",
	"docs/development/testing.md",
	"docs/guides/authoring-topologies.md",
	"docs/guides/heterogeneous-nodes.md",
	"docs/guides/lifecycle-and-reset.md",
	"docs/guides/networking-and-policy.md",
	"docs/guides/troubleshooting.md",
	"docs/index.md",
	"docs/operations/agent-operations.md",
	"docs/operations/artifacts.md",
	"docs/operations/control-plane-deployment.md",
	"docs/operations/upgrades-and-recovery.md",
	"docs/quickstart.md",
	"docs/reference/api.md",
	"docs/reference/cli.md",
	"docs/reference/hcl.md",
}

var markdownLink = regexp.MustCompile(`\[[^]]*\]\(([^)]+)\)`)

func main() {
	actual, err := filepath.Glob("docs/**/*.md")
	check(err)
	top, err := filepath.Glob("docs/*.md")
	check(err)
	actual = append(actual, top...)
	sort.Strings(actual)
	sort.Strings(expectedDocs)
	if strings.Join(actual, "\n") != strings.Join(expectedDocs, "\n") {
		fail("docs tree differs from the maintained documentation set")
	}

	if lines("README.md") > 160 {
		fail("README.md exceeds 160 lines; move detail into docs")
	}

	files := append([]string{"README.md"}, actual...)
	examples, err := filepath.Glob("examples/*/*.md")
	check(err)
	files = append(files, examples...)
	stale := []string{"docs/README.md", "docs/overview.md", "docs/deployment.md", "docs/releasing.md", "docs/firecracker-artifacts.md", "docs/superpowers/", "docs/verification/"}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		check(err)
		text := string(raw)
		for _, old := range stale {
			if strings.Contains(text, old) {
				fail(fmt.Sprintf("%s references retired path %s", file, old))
			}
		}
		for _, match := range markdownLink.FindAllStringSubmatch(text, -1) {
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
	fmt.Printf("documentation checks passed (%d canonical docs)\n", len(actual))
}

func lines(path string) int {
	f, err := os.Open(path)
	check(err)
	defer f.Close()
	n := 0
	s := bufio.NewScanner(f)
	for s.Scan() {
		n++
	}
	check(s.Err())
	return n
}

func check(err error) {
	if err != nil {
		fail(err.Error())
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "docs check:", message)
	os.Exit(1)
}
