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
	"docs/design-principles.md",
	"docs/design/2026-09-06-cidr-functions-var-prescan-design.md",
	"docs/design/2026-09-06-cidr-functions-var-prescan-plan.md",
	"docs/design/2026-09-07-arch-conventions.md",
	"docs/design/2026-09-07-arch-hardening-design.md",
	"docs/design/2026-09-07-arch-hardening-plan.md",
	"docs/design/2026-09-07-firewall-write-only.md",
	"docs/design/2026-09-07-sysbox-gaps-design.md",
	"docs/design/2026-09-07-sysbox-gaps-plan.md",
	"docs/design/2026-09-07-topology-syscall-collection.md",
	"docs/design/2026-09-09-gc-command.md",
	"docs/design/2026-09-10-nsenter-firewall-design.md",
	"docs/design/2026-09-13-heterogeneous-review.md",
	"docs/design/2026-09-13-project-review.md",
	"docs/design/revision-and-upsert-apply-plan.md",
	"docs/design/revision-and-upsert-apply.md",
	"docs/design/revision-directory-tree-addressing.md",
	"docs/design/topology-readiness-contract.md",
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
	"docs/reference/portability.md",
	"docs/reference/resource-model.md",
	"docs/superpowers/plans/2026-09-13-lifecycle-closure.md",
	"docs/superpowers/specs/2026-09-13-lifecycle-closure-design.md",
}

var markdownLink = regexp.MustCompile(`\[[^]]*\]\(([^)]+)\)`)

func main() {
	var allDocs []string
	err := filepath.WalkDir("docs", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(path) == ".md" {
			allDocs = append(allDocs, path)
		}
		return nil
	})
	check(err)
	actual := make([]string, 0, len(allDocs))
	for _, file := range allDocs {
		if !strings.HasPrefix(file, "docs/business/") {
			actual = append(actual, file)
		}
	}
	sort.Strings(actual)
	sort.Strings(expectedDocs)
	if strings.Join(actual, "\n") != strings.Join(expectedDocs, "\n") {
		fail("docs tree differs from the maintained documentation set")
	}

	readmes := []string{"README.md", "README.en.md"}
	for _, readme := range readmes {
		if lines(readme) > 160 {
			fail(fmt.Sprintf("%s exceeds 160 lines; move detail into docs", readme))
		}
	}

	files := append(readmes, actual...)
	examples, err := filepath.Glob("examples/*/*.md")
	check(err)
	files = append(files, examples...)
	stale := []string{"docs/README.md", "docs/overview.md", "docs/deployment.md", "docs/releasing.md", "docs/firecracker-artifacts.md", "docs/superpowers/", "docs/verification/", "docs/sysbox-three-core-challenges.md"}
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
