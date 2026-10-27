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
	"docs/operations/ownership-transfer.md",
	"docs/operations/stage2-capability-fault-ledger.md",
	"docs/operations/stage2-closure.md",
	"docs/operations/stage3-release-integration.md",
	"docs/superpowers/plans/2026-10-26-capability-fault-closure.md",
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
	"docs/design/2026-09-28-interface-change-inventory.md",
	"docs/design/2026-09-28-observation-driven-runtime-refactor.md",
	"docs/design/2026-09-28-runtime-refactor-implementation-status.md",
	"docs/design/2026-10-07-full-migration-distributed-execution-plan.md",
	"docs/design/2026-10-07-parallel-implementation-tracker.md",
	"docs/design/2026-10-10-execution-exclusion.md",
	"docs/design/2026-10-10-managed-ssh-access.md",
	"docs/design/2026-10-10-reset-continuation.md",
	"docs/design/2026-10-12-local-ssh-authority.md",
	"docs/design/2026-10-12-ssh-public-workflow-closure.md",
	"docs/design/2026-10-12-ssh-service-runtime-completion.md",
	"docs/design/2026-10-12-targeted-run-authority.md",
	"docs/design/2026-10-13-reset-ssh-dependencies.md",
	"docs/design/2026-10-13-ssh-boot-deployment.md",
	"docs/design/2026-10-13-unknown-run-recovery.md",
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
	"docs/operations/local-ssh-authority.md",
	"docs/operations/managed-ssh-service.md",
	"docs/operations/p9-real-acceptance.md",
	"docs/operations/two-vm-managed-ssh.md",
	"docs/operations/upgrades-and-recovery.md",
	"docs/quickstart.md",
	"docs/reference/api.md",
	"docs/reference/cli.md",
	"docs/reference/guest-tasks.md",
	"docs/reference/hcl.md",
	"docs/reference/portability.md",
	"docs/reference/resource-model.md",
	"docs/superpowers/plans/2026-09-13-lifecycle-closure.md",
	"docs/superpowers/plans/2026-09-28-observation-driven-runtime-implementation-plan.md",
	"docs/superpowers/plans/2026-10-07-attachment-target-ownership.md",
	"docs/superpowers/plans/2026-10-07-control-plane-closure.md",
	"docs/superpowers/plans/2026-10-07-destroy-receipt.md",
	"docs/superpowers/plans/2026-10-07-guest-readiness.md",
	"docs/superpowers/plans/2026-10-14-provider-quiescence.md",
	"docs/superpowers/plans/2026-10-19-durable-gc-sources.md",
	"docs/superpowers/plans/2026-10-21-three-stage-closure.md",
	"docs/superpowers/plans/2026-10-22-three-pr-operational-closure.md",
	"docs/superpowers/plans/2026-10-27-firecracker-policy.md",
	"docs/superpowers/specs/2026-10-27-guest-adaptation-design.md",
	"docs/superpowers/specs/2026-09-13-lifecycle-closure-design.md",
	"docs/superpowers/specs/2026-09-28-sysbox-architecture-refactor-design.md",
	"docs/superpowers/specs/2026-10-22-operational-recovery-closure-design.md",
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
