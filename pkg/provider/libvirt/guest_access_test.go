package libvirt

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestPrepareGuestNetworkRestoresGeneratedAuthorizedKey(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is unavailable")
	}
	vmDir := t.TempDir()
	privateKey := filepath.Join(vmDir, "guest-ssh-key")
	require.NoError(t, exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", privateKey).Run())
	original := &HandleState{
		DomainName:  "vm",
		VMDir:       vmDir,
		SSHUser:     "ubuntu",
		SSHKey:      privateKey,
		NetworkInit: substrate.GuestNetworkInitCloudInit,
	}
	raw, err := json.Marshal(original)
	require.NoError(t, err)
	var restored HandleState
	require.NoError(t, json.Unmarshal(raw, &restored))
	require.Empty(t, restored.SSHAuthorizedKey)

	bin := t.TempDir()
	userData := filepath.Join(t.TempDir(), "user-data")
	writeExecutable(t, filepath.Join(bin, "genisoimage"), "#!/bin/sh\ncp user-data \"$SYSBOX_TEST_USER_DATA\"\ntouch \"$3\"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SYSBOX_TEST_USER_DATA", userData)
	require.NoError(t, New(t.TempDir()).PrepareGuestNetwork(context.Background(), substrate.NodeHandle{Provider: &restored}))

	publicKey, err := os.ReadFile(privateKey + ".pub")
	require.NoError(t, err)
	generated, err := os.ReadFile(userData)
	require.NoError(t, err)
	require.Contains(t, string(generated), string(publicKey[:len(publicKey)-1]))
}

func TestGuestFileInstallRequestUsesDirectExecution(t *testing.T) {
	request := guestFileInstallRequest("chmod", "0600", "/tmp/payload")

	require.Equal(t, substrate.ShellNone, request.Shell)
	require.NoError(t, request.Validate())
}

func TestPrepareHandlePreservesEphemeralSSHKeyWhenConfigOmitsOne(t *testing.T) {
	handle := substrate.NodeHandle{Provider: &HandleState{
		SSHKey:  "/state/generated-guest-ssh-key",
		SSHUser: "ubuntu",
	}}
	require.NoError(t, New().PrepareHandle(context.Background(), &handle, &Config{SSHUser: "ubuntu"}, nil))
	require.Equal(t, "/state/generated-guest-ssh-key", hsFrom(handle).SSHKey)
}
