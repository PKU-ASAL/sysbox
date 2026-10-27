package windowstask

import (
	"context"
	"github.com/oslab/sysbox/pkg/taskprotocol"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsNativeBootIdentityStable(t *testing.T) {
	p := &WindowsPlatform{}
	first, err := p.BootID()
	require.NoError(t, err)
	require.NotEmpty(t, first)
	second, err := p.BootID()
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Positive(t, p.Uptime())
}
func TestWindowsNativeJobRequiresConfiguredRestrictedAccount(t *testing.T) {
	p := &WindowsPlatform{}
	_, err := p.workerToken()
	require.Error(t, err)
}
func TestWindowsNativeSupervisorContract(t *testing.T) {
	if os.Getenv("SYSBOX_WINDOWS_TASK_E2E") != "1" {
		t.Skip("requires isolated installed Windows helper and dedicated worker account")
	}
	engine, err := LoadWindowsEngine(DefaultConfigPath)
	require.NoError(t, err)
	require.NoError(t, engine.Platform.(*WindowsPlatform).ValidateRoot(engine.Root))
	token, err := engine.Platform.(*WindowsPlatform).workerToken()
	require.NoError(t, err)
	token.Close()
	// Task requests must still carry a signature; local OS readiness grants no authority.
	_, err = engine.Start(context.Background(), taskprotocol.Request{ID: "unsigned", UserID: 65534, Args: []string{"cmd.exe", "/c", "exit 0"}, OutputLimit: 1024})
	require.Error(t, err)
	require.Less(t, time.Duration(0), engine.Platform.Uptime())
}

func TestWindowsNativeRejectsWeakFileACLBelowProtectedRoot(t *testing.T) {
	if os.Getenv("SYSBOX_WINDOWS_TASK_E2E") != "1" {
		t.Skip("requires isolated Windows System test context")
	}
	engine, err := LoadWindowsEngine(DefaultConfigPath)
	require.NoError(t, err)
	p := engine.Platform.(*WindowsPlatform)
	path := filepath.Join(engine.Root, "acl-probe.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"nonsecret":"probe"}`), 0600))
	defer os.Remove(path)
	require.NoError(t, p.ValidateFile(path))
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FR;;;BU)")
	require.NoError(t, err)
	acl, _, err := sd.DACL()
	require.NoError(t, err)
	require.NoError(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil))
	require.ErrorIs(t, p.ValidateFile(path), ErrUnauthorized)
}
