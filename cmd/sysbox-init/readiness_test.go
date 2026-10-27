package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oslab/sysbox/pkg/vsockrpc"
)

func TestGuestReadiness(t *testing.T) {
	for _, tc := range []struct {
		name, init, state, marker   string
		cleanupState, cleanupResult string
		queryErr                    error
		ready                       bool
	}{
		{name: "handoff pending", init: "/sysbox-init", marker: "boot"},
		{name: "systemd starting", init: "/usr/lib/systemd/systemd", state: "activating"},
		{name: "systemd initialized", init: "/usr/lib/systemd/systemd", state: "active", cleanupState: "active", cleanupResult: "success", ready: true},
		{name: "systemd target active but cleanup missing", init: "/usr/lib/systemd/systemd", state: "active"},
		{name: "systemd target active but cleanup failed", init: "/usr/lib/systemd/systemd", state: "active", cleanupState: "failed", cleanupResult: "exit-code"},
		{name: "systemd target active but cleanup pending", init: "/usr/lib/systemd/systemd", state: "active", cleanupState: "activating", cleanupResult: "success"},
		{name: "systemd target active but cleanup result unknown", init: "/usr/lib/systemd/systemd", state: "active", cleanupState: "active"},
		{name: "systemd target failed", init: "/usr/lib/systemd/systemd", state: "failed"},
		{name: "systemd query failed", init: "/usr/lib/systemd/systemd", queryErr: errors.New("bus unavailable")},
		{name: "custom init needs marker", init: "/bin/busybox"},
		{name: "custom init stale marker", init: "/bin/busybox", marker: "old-boot"},
		{name: "custom init ready", init: "/bin/busybox", marker: "boot\n", ready: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := readinessProbe{
				readlink: func(path string) (string, error) {
					if path == "/proc/1/exe" {
						return tc.init, nil
					}
					return "/sysbox-init", nil
				},
				readFile: func(path string) ([]byte, error) {
					if path == bootIDPath {
						return []byte("boot\n"), nil
					}
					if tc.marker == "" {
						return nil, os.ErrNotExist
					}
					return []byte(tc.marker), nil
				},
				unitState: func(ctx context.Context, unit string) (systemdUnitState, error) {
					if _, ok := ctx.Deadline(); !ok {
						t.Error("probe has no deadline")
					}
					if unit == "systemd-tmpfiles-setup.service" {
						return systemdUnitState{active: tc.cleanupState, result: tc.cleanupResult}, tc.queryErr
					}
					return systemdUnitState{active: tc.state}, tc.queryErr
				},
			}
			ready, reason := probe.check()
			if ready != tc.ready || (!ready && reason == "") {
				t.Fatalf("ready=%v reason=%q, want %v", ready, reason, tc.ready)
			}
		})
	}
}

func TestDispatchRejectsEffectsBeforeGuestReady(t *testing.T) {
	for _, op := range []vsockrpc.Op{vsockrpc.OpExec, vsockrpc.OpWriteFile} {
		t.Run(string(op), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "must-not-exist")
			var output bytes.Buffer
			dispatchVsockRequest(nil, &output, bufio.NewReader(strings.NewReader("")), vsockrpc.Request{Op: op, Cmd: []string{"touch", path}, Path: path}, func() (bool, string) { return false, "sysinit target pending" })
			var frame vsockrpc.Frame
			if err := json.Unmarshal(output.Bytes(), &frame); err != nil {
				t.Fatal(err)
			}
			if !frame.Done || !strings.Contains(frame.Error, "guest not ready") {
				t.Fatalf("unexpected reply: %+v", frame)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("operation executed: %v", err)
			}
		})
	}
}

func TestReadinessRPCSeparateFromPing(t *testing.T) {
	for _, op := range []vsockrpc.Op{vsockrpc.OpPing, vsockrpc.OpReady} {
		var output bytes.Buffer
		dispatchVsockRequest(nil, &output, nil, vsockrpc.Request{Op: op}, func() (bool, string) { return false, "initializing" })
		var frame vsockrpc.Frame
		if err := json.Unmarshal(output.Bytes(), &frame); err != nil {
			t.Fatal(err)
		}
		if !frame.Done || frame.Error != "" {
			t.Fatalf("unexpected reply %+v", frame)
		}
		if op == vsockrpc.OpPing && !frame.Pong {
			t.Fatal("ping must remain available")
		}
		if op == vsockrpc.OpReady && (frame.Ready || frame.ReadinessReason != "initializing") {
			t.Fatalf("unexpected readiness %+v", frame)
		}
	}
}

func TestDispatchAllowsEffectsAfterGuestReady(t *testing.T) {
	path := filepath.Join(t.TempDir(), "created")
	var output bytes.Buffer
	dispatchVsockRequest(nil, &output, bufio.NewReader(strings.NewReader("")), vsockrpc.Request{Op: vsockrpc.OpExec, Cmd: []string{"touch", path}}, func() (bool, string) { return true, "" })
	var frame vsockrpc.Frame
	if err := json.Unmarshal(output.Bytes(), &frame); err != nil {
		t.Fatal(err)
	}
	if !frame.Done || frame.Error != "" || frame.ExitCode != 0 {
		t.Fatalf("unexpected reply: %+v", frame)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestReadinessFailsClosedWithoutProcessOrBootEvidence(t *testing.T) {
	for _, missing := range []string{"/proc/1/exe", "/proc/self/exe", bootIDPath} {
		t.Run(missing, func(t *testing.T) {
			probe := readinessProbe{
				readlink: func(path string) (string, error) {
					if path == missing {
						return "", os.ErrPermission
					}
					if path == "/proc/1/exe" {
						return "/bin/busybox", nil
					}
					return "/sysbox-init", nil
				},
				readFile: func(path string) ([]byte, error) {
					if path == missing {
						return nil, os.ErrPermission
					}
					return []byte("boot"), nil
				},
			}
			if ready, reason := probe.check(); ready || reason == "" {
				t.Fatalf("accepted missing %s: %v %q", missing, ready, reason)
			}
		})
	}
}
