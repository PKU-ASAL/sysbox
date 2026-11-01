package guesttask

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestTerminalIO(t *testing.T) {
	for _, tty := range []bool{false, true} {
		t.Run(map[bool]string{false: "pipe", true: "pty"}[tty], func(t *testing.T) {
			root, err := os.MkdirTemp("", "sysbox-term-")
			require.NoError(t, err)
			defer os.RemoveAll(root)
			var out, errOut bytes.Buffer
			command := "read answer; printf 'answer=%s\\n' \"$answer\"; printf 'error\\n' >&2"
			options := substrate.TerminalOptions{TTY: tty}
			if tty {
				options.Cols, options.Rows = 80, 24
				command = "for f in /proc/self/fd/*; do readlink \"$f\"; done; stty size; " + command
			}
			cmd := exec.Command("/bin/sh", "-c", command)
			terminal, err := prepareTerminal(root, cmd, options, &out, &errOut)
			require.NoError(t, err)
			defer terminal.Close()
			require.NoError(t, cmd.Start())
			terminal.Start()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if tty {
				require.NoError(t, sendTerminalControl(ctx, filepath.Join(root, "terminal.sock"), substrate.TerminalControl{Type: "resize", Cols: 100, Rows: 40}))
			}
			require.NoError(t, sendTerminalControl(ctx, filepath.Join(root, "terminal.sock"), substrate.TerminalControl{Type: "stdin", Data: []byte("hello\n")}))
			require.NoError(t, cmd.Wait())
			require.NoError(t, terminal.Drain())
			require.NotContains(t, out.String(), "/dev/pts/ptmx", "child must not inherit the PTY master")
			require.Contains(t, out.String(), "answer=hello")
			if tty {
				require.Contains(t, out.String(), "error")
				require.Empty(t, errOut.String())
			} else {
				require.Contains(t, errOut.String(), "error")
			}
		})
	}
}

func TestRealTerminalTaskLifecycle(t *testing.T) {
	t.Setenv("SYSBOX_GUESTTASK_TEST_HELPER", "1")
	cgroup := os.Getenv("SYSBOX_GUESTTASK_TEST_CGROUP")
	if cgroup == "" {
		t.Skip("requires disposable writable cgroup-v2 delegation")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	root, err := os.MkdirTemp("", "sysbox-terminal-")
	require.NoError(t, err)
	defer os.RemoveAll(root)
	cgroup = filepath.Join(cgroup, filepath.Base(root))
	require.NoError(t, os.Mkdir(cgroup, 0700))
	defer func() {
		entries, _ := os.ReadDir(cgroup)
		for _, entry := range entries {
			if entry.IsDir() {
				_ = os.Remove(filepath.Join(cgroup, entry.Name()))
			}
		}
		_ = os.Remove(cgroup)
	}()
	e := Engine{Root: root, CgroupRoot: cgroup, PublicKey: public, WorkerBinary: os.Args[0], WorkerArgs: []string{"-test.run=^TestTaskWorkerProcess$"}}
	boot, err := BootID()
	require.NoError(t, err)
	for index, scenario := range []string{"tty_exit", "pipe_eof", "cancel", "expiry", "output_limit"} {
		t.Run(scenario, func(t *testing.T) {
			request := signedRequest(t, private, boot, scenario)
			request.Terminal = &substrate.TerminalOptions{TTY: scenario != "pipe_eof"}
			if request.Terminal.TTY {
				request.Terminal.Cols, request.Terminal.Rows = 80, 24
			}
			request.Args = []string{"/bin/sh", "-c", "sleep 60 & child=$!; read answer; printf 'uid=%s answer=%s child=%s\\n' \"$(id -u)\" \"$answer\" \"$child\"; exit 7"}
			if scenario == "tty_exit" {
				request.Args[2] = "stty size; " + request.Args[2]
			}
			if scenario == "pipe_eof" {
				request.Args[2] = "cat; printf 'EOF\\n'; exit 7"
			}
			if scenario == "output_limit" {
				request.Args[2] = "while :; do printf 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'; done"
			}
			g := request.Grant.Grant
			g.Epoch = uint64(index + 1)
			if scenario == "expiry" {
				g.ExpiresAt = time.Now().Add(2 * time.Second)
			}
			g.Digest, err = request.Digest()
			require.NoError(t, err)
			request.Grant, err = controlplane.SignExecutionGrant(private, g, boot, time.Now())
			require.NoError(t, err)
			r, err := e.Start(context.Background(), request)
			require.NoError(t, err)
			defer e.Cancel(context.Background(), r.ID, r.Digest)
			if scenario != "output_limit" {
				require.Eventually(t, func() bool {
					r, err = e.Observe(context.Background(), request.ID, g.Digest)
					return err == nil && r.Status == "running"
				}, 5*time.Second, 20*time.Millisecond, "last record: %+v, error=%v", r, err)
			}
			control := func(c substrate.TerminalControl) error {
				return e.Control(context.Background(), Call{ID: request.ID, Digest: g.Digest, Grant: &request.Grant, Control: &c})
			}
			if scenario == "tty_exit" {
				require.NoError(t, control(substrate.TerminalControl{Type: "resize", Cols: 100, Rows: 40}))
				require.NoError(t, control(substrate.TerminalControl{Type: "stdin", Data: []byte("hello\n")}))
			}
			if scenario == "pipe_eof" {
				require.NoError(t, control(substrate.TerminalControl{Type: "stdin", Data: []byte("hello\n")}))
				require.NoError(t, control(substrate.TerminalControl{Type: "eof"}))
			}
			if scenario == "cancel" {
				require.NoError(t, e.Cancel(context.Background(), r.ID, r.Digest))
			}
			require.Eventually(t, func() bool {
				r, err = e.Observe(context.Background(), request.ID, g.Digest)
				return err == nil && r.TerminationConfirmed
			}, 10*time.Second, 20*time.Millisecond)
			group, err := e.openGroup(r)
			require.NoError(t, err)
			defer group.Close()
			empty, err := groupEmpty(group)
			require.NoError(t, err)
			require.True(t, empty, "all descendants must be absent")
			if scenario == "tty_exit" || scenario == "pipe_eof" {
				require.True(t, r.ExitCodeKnown)
				require.Equal(t, 7, r.ExitCode)
				out, _, err := e.ReadOutput(context.Background(), r.ID, r.Digest, "stdout", 0, 4096)
				require.NoError(t, err)
				require.Contains(t, string(out), "hello")
				if scenario == "tty_exit" {
					require.Contains(t, string(out), "uid=65534")
				}
			} else {
				require.True(t, r.CancelRequested)
				require.False(t, r.ExitCodeKnown)
			}
			if scenario == "output_limit" {
				require.True(t, r.StdoutTruncated)
			}
			replayed, err := e.Start(context.Background(), request)
			if scenario != "expiry" {
				require.NoError(t, err)
				require.Equal(t, r.PID, replayed.PID)
			}
			require.Error(t, control(substrate.TerminalControl{Type: "stdin", Data: []byte("again\n")}))
			t.Logf("%s: exit_known=%v stopped=%v pid=%d", scenario, r.ExitCodeKnown, r.TerminationConfirmed, r.PID)
		})
	}
}

func TestTerminalBlockedInputDoesNotPreventShutdown(t *testing.T) {
	root, err := os.MkdirTemp("", "sysbox-term-")
	require.NoError(t, err)
	defer os.RemoveAll(root)
	cmd := exec.Command("/bin/sleep", "30")
	terminal, err := prepareTerminal(root, cmd, substrate.TerminalOptions{}, io.Discard, io.Discard)
	require.NoError(t, err)
	defer terminal.Close()
	require.NoError(t, cmd.Start())
	defer cmd.Process.Kill()
	terminal.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for i := 0; i < 16; i++ {
		err = sendTerminalControl(ctx, filepath.Join(root, "terminal.sock"), substrate.TerminalControl{Type: "stdin", Data: bytes.Repeat([]byte("x"), substrate.MaxTerminalInput)})
		if err != nil {
			break
		}
	}
	require.Error(t, err, "input must be bounded when the guest does not read")
	done := make(chan struct{})
	go func() { terminal.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("terminal shutdown blocked on input")
	}
	require.NoError(t, cmd.Process.Kill())
	_ = cmd.Wait()
}
