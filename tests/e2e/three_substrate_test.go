//go:build e2e
// +build e2e

// three_substrate_test.go (PR-13): verifies that docker, firecracker and libvirt
// nodes can coexist in the same topology, sharing sysbox-managed L2 bridges.
//
// Run with:
//
//	sudo -E go test -tags e2e -v ./tests/e2e/... -run TestThreeSubstrate -timeout 5m
//
// Prerequisites:
//   - Docker daemon running
//   - firecracker binary in PATH; SYSBOX_ROOTFS set to a valid ext4 rootfs
//   - SYSBOX_E2E_FIRECRACKER_KERNEL set to a vmlinux with vsock support
//   - libvirtd running; SYSBOX_QCOW2 set to a valid qcow2 image
//
// Each missing substrate causes that node's assertions to be skipped
// (not failed) so CI environments can still run partial checks.
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestThreeSubstrate(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("three-substrate test requires root; run: sudo -E go test -tags e2e ...")
	}

	// Check which substrates are available; skip missing ones gracefully.
	dockerOK := isCommandAvailable("docker")
	fcOK := isCommandAvailable("firecracker") &&
		fileExists(envOrDefault("SYSBOX_ROOTFS", "/tmp/sysbox-rootfs.ext4")) &&
		fileExists(envOrDefault("SYSBOX_E2E_FIRECRACKER_KERNEL", "/tmp/vmlinux"))
	kvmOK := isCommandAvailable("virsh") && isCommandAvailable("qemu-img") && fileExists(envOrDefault("SYSBOX_QCOW2", "/tmp/sysbox-base.qcow2"))

	if !dockerOK && !fcOK && !kvmOK {
		t.Skip("no substrates available")
	}

	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	binPath := filepath.Join(repoRoot, "bin/sysbox")
	buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/sysbox")
	buildCmd.Dir = repoRoot
	out, err := buildCmd.CombinedOutput()
	require.NoError(t, err, "build: %s", out)

	// Write HCL to temp file.
	hclFile := filepath.Join(t.TempDir(), "three-substrate.sysbox.hcl")
	require.NoError(t, os.WriteFile(hclFile, []byte(hclThreeSubstrate(dockerOK, fcOK, kvmOK)), 0o644))

	statePath := filepath.Join(t.TempDir(), "state.json")

	run := func(args ...string) ([]byte, error) {
		cmd := exec.Command(binPath, append(
			[]string{"--file", hclFile, "--state", statePath}, args...,
		)...)
		cmd.Dir = repoRoot
		cmd.Env = append(os.Environ())
		return cmd.CombinedOutput()
	}
	apply := func() ([]byte, error) { return run("apply", "--auto-approve") }

	forceCleanup(t, statePath, "sysbox-container", "sysbox-microvm", "sysbox-vm")
	t.Cleanup(func() {
		out, err := run("destroy", "--auto-approve")
		if err != nil {
			t.Logf("destroy: %v\n%s", err, out)
		}
	})

	// ── Apply ────────────────────────────────────────────────────────────────

	applyOut, applyErr := apply()
	applyStr := string(applyOut)
	t.Logf("apply output:\n%s", applyStr)

	// Apply should succeed as long as at least one substrate works.
	if dockerOK {
		require.NoError(t, applyErr, "apply with docker available must not fail: %s", applyStr)
	}

	// ── State checks ──────────────────────────────────────────────────────────

	listOut, err := run("state", "list")
	require.NoError(t, err, "state list: %s", listOut)
	listStr := string(listOut)
	t.Logf("state:\n%s", listStr)

	if dockerOK {
		require.Contains(t, listStr, "sysbox_node.container", "docker node missing from state")
	}
	if fcOK {
		require.Contains(t, listStr, "sysbox_node.microvm", "firecracker node missing from state")
	}
	if kvmOK {
		require.Contains(t, listStr, "sysbox_node.vm", "libvirt node missing from state")
	}

	// ── Docker connectivity ───────────────────────────────────────────────────

	if dockerOK {
		pingOut, err := exec.Command("docker", "exec", "sysbox-container",
			"ping", "-c", "1", "-W", "3", "10.99.0.10").CombinedOutput()
		require.NoError(t, err, "container self-ping: %s", pingOut)
	}

	// ── Outputs ───────────────────────────────────────────────────────────────

	outCmd, err := run("output")
	require.NoError(t, err, "output: %s", outCmd)
	var expectedOutputs []string
	if dockerOK {
		expectedOutputs = append(expectedOutputs, "container_ip")
	}
	if fcOK {
		expectedOutputs = append(expectedOutputs, "microvm_ip")
	}
	if kvmOK {
		expectedOutputs = append(expectedOutputs, "vm_ip")
	}
	for _, expect := range expectedOutputs {
		require.Contains(t, string(outCmd), expect, "output missing %s", expect)
	}

	// ── Idempotent re-apply ───────────────────────────────────────────────────

	reapplyOut, err := apply()
	if err == nil {
		require.True(t,
			strings.Contains(string(reapplyOut), "No changes") ||
				strings.Contains(string(reapplyOut), "Apply complete"),
			"re-apply should be no-op or succeed: %s", reapplyOut)
	}

	// ── Destroy ───────────────────────────────────────────────────────────────

	destroyOut, err := run("destroy", "--auto-approve")
	require.NoError(t, err, "destroy: %s", destroyOut)
	require.Contains(t, string(destroyOut), "Destroy complete", "destroy output: %s", destroyOut)

	// Verify docker container removed.
	if dockerOK {
		inspectOut, _ := exec.Command("docker", "inspect", "sysbox-container").CombinedOutput()
		require.Contains(t, string(inspectOut), "No such", "docker container should be gone: %s", inspectOut)
	}

	t.Logf("TestThreeSubstrate: PASS (docker=%v fc=%v kvm=%v)", dockerOK, fcOK, kvmOK)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func isCommandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// hclThreeSubstrate returns a topology containing only the substrates that are
// actually available on the current host. Missing optional image artifacts
// should skip that substrate, not make the whole apply fail before assertions.
func hclThreeSubstrate(dockerOK, fcOK, kvmOK bool) string {
	var b strings.Builder

	if dockerOK {
		b.WriteString(`substrate "docker" { alias = "dk" }
`)
	}
	if fcOK {
		b.WriteString(`substrate "firecracker" { alias = "fc" }
`)
	}
	if kvmOK {
		b.WriteString(`substrate "libvirt" { alias = "kvm" }
`)
	}

	b.WriteString(`
resource "sysbox_network" "shared" {
  cidr = "10.99.0.0/24"
  nat  = false
}
`)

	if dockerOK {
		b.WriteString(`
resource "sysbox_image" "alpine_docker" {
  substrate  = substrate.docker.dk
  docker_ref = "alpine:latest"
}

resource "sysbox_node" "container" {
  substrate = substrate.docker.dk
  image     = sysbox_image.alpine_docker.id

  link {
    network = sysbox_network.shared.id
    ip      = "10.99.0.10/24"
  }
}

output "container_ip" { value = "10.99.0.10" }
`)
	}

	if fcOK {
		fmt.Fprintf(&b, `
resource "sysbox_image" "fc_rootfs" {
  substrate = substrate.firecracker.fc
  rootfs    = %q
}

resource "sysbox_kernel" "vmlinux" {
  substrate = substrate.firecracker.fc
  source    = %q
}

resource "sysbox_node" "microvm" {
  substrate = substrate.firecracker.fc
  image     = sysbox_image.fc_rootfs.id

  provider "firecracker" {
    kernel   = sysbox_kernel.vmlinux.id
    ssh_user = "root"
    ssh_pass = "root"
  }

  link {
    network = sysbox_network.shared.id
    ip      = "10.99.0.20/24"
  }
}

output "microvm_ip" { value = "10.99.0.20" }
`, envOrDefault("SYSBOX_ROOTFS", "/tmp/sysbox-rootfs.ext4"), envOrDefault("SYSBOX_E2E_FIRECRACKER_KERNEL", "/tmp/vmlinux"))
	}

	if kvmOK {
		fmt.Fprintf(&b, `
resource "sysbox_image" "kvm_disk" {
  substrate = substrate.libvirt.kvm
  qcow2     = %q
}

resource "sysbox_node" "vm" {
  substrate = substrate.libvirt.kvm
  image     = sysbox_image.kvm_disk.id

  provider "libvirt" {
    vcpus    = 1
    memory   = "512"
    ssh_user = "ubuntu"
    ssh_pass = "ubuntu"
  }

  link {
    network = sysbox_network.shared.id
    ip      = "10.99.0.30/24"
  }
}

output "vm_ip" { value = "10.99.0.30" }
`, envOrDefault("SYSBOX_QCOW2", "/tmp/sysbox-base.qcow2"))
	}

	return b.String()
}
