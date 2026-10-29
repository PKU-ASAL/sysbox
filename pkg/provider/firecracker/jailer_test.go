package firecracker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestFirecrackerRejectsDirectLaunchOption(t *testing.T) {
	body, diags := hclsyntax.ParseConfig([]byte("allow_direct = true\n"), "node.hcl", hcl.InitialPos)
	require.False(t, diags.HasErrors())
	_, err := New("/kernel", t.TempDir()).DecodeProviderConfig(body.Body, nil)
	require.Error(t, err, "direct launch is no longer a supported execution path")
}

func TestJailerRefusesMissingTrustBeforeLaunch(t *testing.T) {
	s, h, _, _ := manifestFixture(t, false)
	m, err := s.loadManifest(context.Background(), h)
	require.NoError(t, err)
	_, err = s.prepareJailer(context.Background(), m)
	require.Error(t, err)
	require.Zero(t, m.Process.PID)
	_, err = trustedExecutable("relative-jailer")
	require.Error(t, err)
	_, err = trustedExecutable(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
}

func TestJailerRejectsWritableBinaryAncestor(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0777))
	require.Error(t, trustedAncestors(dir))
}

func TestJailArgumentsRejectForeignPaths(t *testing.T) {
	m := &vmManifest{VMID: "fc-test"}
	valid := jailIdentity{Root: "/j/firecracker/fc-test/root", UID: 200001, GID: 200001, Cgroup: "sysbox/fc-test", Executable: "/usr/local/bin/firecracker"}
	for _, change := range []func(*jailIdentity){
		func(j *jailIdentity) { j.Cgroup = "/sys/fs/cgroup" },
		func(j *jailIdentity) { j.Cgroup = "../fc-test" },
		func(j *jailIdentity) { j.Cgroup = "sysbox/another" },
		func(j *jailIdentity) { j.Root = "/j/firecracker/other/root" },
		func(j *jailIdentity) { j.GID = 0 },
	} {
		j := valid
		change(&j)
		_, err := jailerArguments(m, j)
		require.Error(t, err)
	}
}

func TestFencedGCRequiresJailResidueAbsence(t *testing.T) {
	s := New("/kernel", t.TempDir())
	s.jailBase = t.TempDir()
	s.firecrackerBin = "/usr/local/bin/firecracker"
	s.cgroupParent = "sysbox-jailer-missing-test"
	root := filepath.Join(s.jailBase, "firecracker", "vm", "root")
	require.NoError(t, os.MkdirAll(root, 0700))
	_, err := s.InspectFencedGC(context.Background(), "vm", "owner", "node", 1)
	require.ErrorContains(t, err, "jail generation residue")
}

func TestFirecrackerConfigurationDoesNotRequireUnsafeOptIn(t *testing.T) {
	s := New("/kernel", t.TempDir())
	require.NoError(t, s.Validate(substrate.NodeSpec{ProviderConfig: &Config{}}))
}

func TestJailerRequestSeparatesHostAndGuestPaths(t *testing.T) {
	m := &vmManifest{VMID: "fc-test", Handle: HandleState{VMDir: "/var/lib/sysbox/meta/top/node/1", NetnsName: "lab-net"}}
	j := jailIdentity{Root: "/var/lib/sysbox/jails/firecracker/fc-test/root", UID: 100000, GID: 100000, Cgroup: "sysbox/firecracker/fc-test", Executable: "/usr/local/bin/firecracker"}
	args, err := jailerArguments(m, j)
	require.NoError(t, err)
	require.Contains(t, args, "--uid")
	require.Contains(t, args, "100000")
	require.Contains(t, args, "--cgroup-version")
	require.Contains(t, args, "2")
	require.Contains(t, args, "/run/netns/lab-net")
	require.NotContains(t, args, "--daemonize")
	require.Contains(t, args, "/vm_config.json")
	require.Contains(t, args, "/run/firecracker.sock")
	require.NotContains(t, args, m.Handle.VMDir)
	j.UID = 0
	_, err = jailerArguments(m, j)
	require.Error(t, err)
}
