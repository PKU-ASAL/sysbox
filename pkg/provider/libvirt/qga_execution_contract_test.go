package libvirt

import (
	"bytes"
	"context"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQGAExecHonorsTimeoutWithoutClaimingRemoteStop(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "virsh"), []byte("#!/bin/sh\nexec sleep 5\n"), 0700))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	started := time.Now()
	result, err := (&qgaConnection{domain: "uuid"}).Exec(context.Background(), substrate.ExecRequest{Program: "true", Shell: substrate.ShellNone, Timeout: 20 * time.Millisecond}, nil, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), time.Second)
	require.Equal(t, substrate.RemoteTerminationUnknown, result.RemoteTermination)
	require.False(t, result.ExitCodeKnown)
}
func TestQGAExecBoundsReturnedAndForwardedOutput(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "virsh"), []byte(`#!/bin/sh
case "$*" in
 *guest-exec-status*) echo '{"return":{"exited":true,"exitcode":0,"out-data":"YWJjZGVm","err-data":"Z2hpamts"}}';;
 *) echo '{"return":{"pid":42}}';;
esac
`), 0700))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	var out, errOut bytes.Buffer
	result, err := (&qgaConnection{domain: "uuid"}).Exec(context.Background(), substrate.ExecRequest{Program: "true", Shell: substrate.ShellNone, OutputLimitBytes: 3}, &out, &errOut)
	require.NoError(t, err)
	require.Equal(t, "abc", result.Stdout)
	require.Equal(t, "ghi", result.Stderr)
	require.Equal(t, result.Stdout, out.String())
	require.Equal(t, result.Stderr, errOut.String())
}
