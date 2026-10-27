package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	bootIDPath    = "/proc/sys/kernel/random/boot_id"
	bootReadyPath = "/run/sysbox/boot-ready"
)

// Readiness is distinct from RPC liveness. In particular, sysbox-init starts
// serving before handing PID 1 to the real init, whose boot-time cleanup can
// still remove uploaded files. Non-systemd images must publish bootReadyPath
// containing the current boot ID after their own filesystem initialization.
type readinessProbe struct {
	readlink  func(string) (string, error)
	readFile  func(string) ([]byte, error)
	unitState func(context.Context, string) (systemdUnitState, error)
}

type systemdUnitState struct{ active, result string }

func guestReadiness() (bool, string) {
	return (readinessProbe{
		readlink: os.Readlink,
		readFile: os.ReadFile,
		unitState: func(ctx context.Context, unit string) (systemdUnitState, error) {
			cmd := exec.CommandContext(ctx, "systemctl", "show", unit, "--property=ActiveState", "--property=Result")
			cmd.WaitDelay = 200 * time.Millisecond
			out, err := cmd.CombinedOutput()
			if err != nil {
				return systemdUnitState{}, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
			}
			var state systemdUnitState
			for _, line := range strings.Split(string(out), "\n") {
				key, value, ok := strings.Cut(line, "=")
				if !ok {
					continue
				}
				switch key {
				case "ActiveState":
					state.active = value
				case "Result":
					state.result = value
				}
			}
			return state, nil
		},
	}).check()
}

func (p readinessProbe) check() (bool, string) {
	init, err := p.readlink("/proc/1/exe")
	if err != nil {
		return false, fmt.Sprintf("inspect PID 1: %v", err)
	}
	self, err := p.readlink("/proc/self/exe")
	if err != nil {
		return false, fmt.Sprintf("inspect guest agent: %v", err)
	}
	if init == self {
		return false, "waiting for PID 1 handoff"
	}
	if filepath.Base(init) == "systemd" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		state, err := p.unitState(ctx, "sysinit.target")
		if err != nil {
			return false, fmt.Sprintf("query sysinit.target: %v", err)
		}
		if state.active != "active" {
			return false, fmt.Sprintf("sysinit.target is %q", state.active)
		}
		// sysinit.target only Wants this unit; the target can be active even
		// when boot cleanup failed. Require positive completion evidence.
		cleanup, err := p.unitState(ctx, "systemd-tmpfiles-setup.service")
		if err != nil {
			return false, fmt.Sprintf("query systemd-tmpfiles-setup.service: %v", err)
		}
		if cleanup.active != "active" || cleanup.result != "success" {
			return false, fmt.Sprintf("systemd-tmpfiles-setup.service is %q (result %q)", cleanup.active, cleanup.result)
		}
		return true, ""
	}
	boot, err := p.readFile(bootIDPath)
	if err != nil || strings.TrimSpace(string(boot)) == "" {
		return false, "current boot ID unavailable"
	}
	marker, err := p.readFile(bootReadyPath)
	if err != nil {
		return false, fmt.Sprintf("custom init must publish %s after initialization: %v", bootReadyPath, err)
	}
	if strings.TrimSpace(string(marker)) != strings.TrimSpace(string(boot)) {
		return false, "boot-ready marker does not match current boot ID"
	}
	return true, ""
}
