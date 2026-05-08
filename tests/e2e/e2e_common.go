//go:build e2e
// +build e2e

package e2e

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// forceCleanup removes any leftover sysbox artifacts from previous runs:
// containers, network namespaces, and the state file. Call at the start of
// each e2e test so a prior crash doesn't poison the next run.
func forceCleanup(t *testing.T, statePath string, containerNames ...string) {
	t.Helper()

	// Force-remove named containers.
	for _, name := range containerNames {
		cmd := exec.Command("docker", "rm", "-f", name)
		_, _ = cmd.CombinedOutput()
	}

	// Remove all netns with sysbox- prefix.
	out, _ := exec.Command("ip", "netns", "list").CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		ns := fields[0]
		if strings.HasPrefix(ns, "sysbox-") {
			exec.Command("ip", "netns", "del", ns).Run()
		}
	}

	// Remove leftover state file so plan starts clean.
	_ = os.Remove(statePath)
	_ = os.Remove(statePath + ".lock")
	_ = os.Remove(statePath + ".tmp")
}
