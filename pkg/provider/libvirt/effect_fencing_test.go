package libvirt

import (
	"context"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLifecycleRejectsStaleGenerationBeforeVirshMutation(t *testing.T) {
	for _, name := range []string{"stop", "destroy", "pause", "resume"} {
		t.Run(name, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "mutations")
			t.Setenv("SYSBOX_FENCE_LOG", log)
			virshFixture(t, "#!/bin/sh\ncase \"$1\" in\nlist) echo 11111111-2222-4333-8444-555555555555;;\ndomstate) echo running;;\n*) echo mutation >> \"$SYSBOX_FENCE_LOG\";;\nesac\n")
			actual := managedOperation()
			old := actual
			old.Generation--
			h := managedNodeHandle(&HandleState{DomainName: "node", DomainUUID: "11111111-2222-4333-8444-555555555555", Identity: &actual})
			s := New(t.TempDir())
			ctx := substrate.WithOperationIdentity(context.Background(), old)
			var err error
			switch name {
			case "stop":
				err = s.StopNode(ctx, h)
			case "destroy":
				err = s.DestroyNode(ctx, h)
			case "pause":
				err = s.Pause(ctx, h)
			case "resume":
				err = s.Resume(ctx, h)
			}
			require.Error(t, err)
			_, err = os.Stat(log)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestEffectsCheckActualDomainMetadata(t *testing.T) {
	for _, fault := range []string{"none", "generation", "owner", "uuid", "missing", "unavailable"} {
		t.Run(fault, func(t *testing.T) {
			id := managedOperation()
			hs := &HandleState{DomainName: "node", DomainUUID: "11111111-2222-4333-8444-555555555555", Identity: &id}
			h := managedNodeHandle(hs)
			actual := *hs
			other := id
			actual.Identity = &other
			switch fault {
			case "generation":
				other.Generation++
			case "owner":
				other.OwnerUID = "other"
			case "uuid":
				actual.DomainUUID = "99999999-2222-4333-8444-555555555555"
			case "missing":
				actual.Identity = nil
			}
			setLiveResetDomain(t, &actual, actual.DomainUUID)
			t.Setenv("SYSBOX_FENCE_UUID", h.ID)
			t.Setenv("SYSBOX_FENCE_FAULT", fault)
			log := filepath.Join(t.TempDir(), "mutations")
			t.Setenv("SYSBOX_FENCE_LOG", log)
			virshFixture(t, `#!/bin/sh
case "$1" in
 list) echo "$SYSBOX_FENCE_UUID";;
 dumpxml) [ "$2" = "$SYSBOX_FENCE_UUID" ] || exit 1; [ "$SYSBOX_FENCE_FAULT" != unavailable ] || exit 1; cat "$SYSBOX_RESET_DOMAIN_XML";;
 *) echo "$*" >> "$SYSBOX_FENCE_LOG";;
esac
`)
			err := New(t.TempDir()).Pause(context.Background(), h)
			if fault == "none" {
				require.NoError(t, err)
				raw, err := os.ReadFile(log)
				require.NoError(t, err)
				require.Equal(t, "suspend "+h.ID+"\n", string(raw))
			} else {
				require.Error(t, err)
				_, err = os.Stat(log)
				require.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}

func TestExecutionSPIFencesBeforeSSH(t *testing.T) {
	for _, operation := range []string{"exec", "start", "observe", "cancel", "copy", "console"} {
		t.Run(operation, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "effects")
			t.Setenv("SYSBOX_FENCE_LOG", log)
			bin := t.TempDir()
			for _, name := range []string{"ip", "ssh", "scp", "sshpass"} {
				writeExecutable(t, filepath.Join(bin, name), "#!/bin/sh\necho effect >> \"$SYSBOX_FENCE_LOG\"\nexit 1\n")
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			id := managedOperation()
			h := managedNodeHandle(&HandleState{DomainUUID: "11111111-2222-4333-8444-555555555555", Identity: &id, SSHIP: "192.0.2.1", Bridges: []BridgeAttach{{Netns: "test-only"}}})
			id.Generation--
			ctx := substrate.WithOperationIdentity(context.Background(), id)
			sub := New(t.TempDir())
			conn, err := sub.Connection(h, nil)
			require.NoError(t, err)
			req := substrate.ExecRequest{Program: "true", Shell: substrate.ShellNone, Timeout: time.Second}
			switch operation {
			case "exec":
				_, err = conn.Exec(ctx, req, io.Discard, io.Discard)
			case "start":
				_, err = conn.(substrate.ConnectionStarter).Start(ctx, req)
			case "observe":
				_, err = conn.(substrate.ConnectionExecutionObserver).ObserveExecution(ctx, substrate.ExecutionHandle{})
			case "cancel":
				_, err = conn.(substrate.ConnectionExecutionCanceler).CancelExecution(ctx, substrate.ExecutionHandle{})
			case "copy":
				err = conn.(substrate.ConnectionFileCopier).CopyFile(ctx, "source", "target")
			case "console":
				conn, connectErr := sub.Connection(h, nil)
				err = connectErr
				if err == nil {
					err = conn.(*domainConnection).ControlExecution(ctx, substrate.ExecutionHandle{}, substrate.TerminalControl{Type: "stdin", Data: []byte("x")})
				}
			}
			require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "expected identity conflict, got %v", err)
			_, err = os.Stat(log)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestAbsentDomainDoesNotAllowStaleDirectoryCleanup(t *testing.T) {
	sub, ctx, request := managedResetFixture(t)
	// The reset target context does not authorize ordinary deletion of the old node.
	err := sub.DestroyNode(ctx, request.Current)
	require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
	require.FileExists(t, hsFrom(request.Current).DiskPath)
	old := *hsFrom(request.Current).Identity
	require.NoError(t, sub.DestroyNode(substrate.WithOperationIdentity(ctx, old), request.Current))
	require.NoDirExists(t, hsFrom(request.Current).VMDir)
}

func TestStartRejectsInconsistentHandleBeforeQuery(t *testing.T) {
	for _, fault := range []string{"generation", "uuid", "identity"} {
		t.Run(fault, func(t *testing.T) {
			id := managedOperation()
			hs := &HandleState{DomainUUID: "11111111-2222-4333-8444-555555555555", Identity: &id}
			h := managedNodeHandle(hs)
			switch fault {
			case "generation":
				h.Generation--
			case "uuid":
				h.ID = "99999999-2222-4333-8444-555555555555"
			case "identity":
				hs.Identity = nil
			}
			err := New(t.TempDir()).StartNode(context.Background(), h)
			require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
		})
	}
}

func TestLibvirtRejectsUnidentifiedMutation(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("SYSBOX_FENCE_LOG", log)
	virshFixture(t, "#!/bin/sh\necho call >> \"$SYSBOX_FENCE_LOG\"\nexit 0\n")
	sub := New(t.TempDir())
	_, err := sub.CreateNode(context.Background(), substrate.NodeSpec{Name: "unowned", Image: substrate.ArtifactHandle{ID: "base"}})
	require.ErrorContains(t, err, "identity")
	h := substrate.NodeHandle{ID: "11111111-2222-4333-8444-555555555555"}
	for _, call := range []func(context.Context, substrate.NodeHandle) error{sub.StartNode, sub.StopNode, sub.DestroyNode, sub.Pause, sub.Resume} {
		require.Error(t, call(context.Background(), h))
	}
	require.NoFileExists(t, log)
}

func TestExecutionTimeoutIncludesDomainQuery(t *testing.T) {
	virshFixture(t, "#!/bin/sh\nexec sleep 5\n")
	id := managedOperation()
	h := managedNodeHandle(&HandleState{DomainUUID: "11111111-2222-4333-8444-555555555555", Identity: &id, SSHIP: "192.0.2.1"})
	conn, err := New(t.TempDir()).Connection(h, nil)
	require.NoError(t, err)
	req := substrate.ExecRequest{Program: "true", Shell: substrate.ShellNone, Timeout: 20 * time.Millisecond}
	start := time.Now()
	result, err := conn.Exec(context.Background(), req, io.Discard, io.Discard)
	require.Error(t, err)
	require.Equal(t, substrate.ExecErrorTimeout, result.ErrorClass)
	require.Less(t, time.Since(start), 2*time.Second)
	start = time.Now()
	_, err = conn.(substrate.ConnectionStarter).Start(context.Background(), req)
	require.Error(t, err)
	require.Less(t, time.Since(start), 2*time.Second)
}
