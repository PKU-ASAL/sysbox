package sshaccess

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func validateSSHDArguments(args []string, config string) error {
	// OpenSSH rewrites argv into its listener title after startup. Accept only
	// that listener prefix; session children do not have this title.
	if len(args) == 1 && strings.HasPrefix(args[0], "sshd: ") {
		end := strings.Index(args[0], " [listener]")
		if end < 0 {
			return fmt.Errorf("sshd process is not a listener")
		}
		args = strings.Fields(strings.TrimPrefix(args[0][:end], "sshd: "))
	}
	selected, foreground := "", false
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "-D":
			foreground = true
		case args[i] == "-e":
		case strings.HasPrefix(args[i], "-f"):
			if selected != "" {
				return fmt.Errorf("duplicate sshd configuration")
			}
			selected = strings.TrimPrefix(args[i], "-f")
			if selected == "" && i+1 < len(args) {
				i++
				selected = args[i]
			}
		default:
			return fmt.Errorf("unsupported sshd process argument")
		}
	}
	if !foreground || selected != config {
		return fmt.Errorf("sshd process configuration mismatch")
	}
	return nil
}

// processController binds reload to an already running, explicitly configured
// sshd process. pidfd signaling prevents PID reuse from selecting another process.
func processController(p ServiceProfile) (func(context.Context, string) error, error) {
	raw, err := readDeploymentFile(p.PIDFile)
	if err != nil {
		return nil, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid < 1 {
		return nil, fmt.Errorf("invalid sshd PID file")
	}
	anchor := func() (string, error) {
		current, err := readDeploymentFile(p.PIDFile)
		if err != nil || strings.TrimSpace(string(current)) != strconv.Itoa(pid) {
			return "", fmt.Errorf("sshd PID file changed")
		}
		wanted, err := os.Stat(p.SSHD)
		if err != nil {
			return "", err
		}
		actual, err := os.Stat(fmt.Sprintf("/proc/%d/exe", pid))
		if err != nil || !os.SameFile(wanted, actual) {
			return "", fmt.Errorf("sshd executable changed")
		}
		cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			return "", err
		}
		args := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
		if err := validateSSHDArguments(args, p.ConfigPath); err != nil {
			return "", err
		}
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return "", err
		}
		end := strings.LastIndexByte(string(stat), ')')
		if end < 0 {
			return "", fmt.Errorf("invalid sshd process anchor")
		}
		fields := strings.Fields(string(stat)[end+1:])
		if len(fields) < 20 {
			return "", fmt.Errorf("incomplete sshd process anchor")
		}
		return fields[19], nil
	}
	start, err := anchor()
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, operation string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		fd, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			return err
		}
		defer unix.Close(fd)
		current, err := anchor()
		if err != nil || current != start {
			return fmt.Errorf("sshd process identity changed: %v", err)
		}
		if operation == "reload" {
			return unix.PidfdSendSignal(fd, unix.SIGHUP, nil, 0)
		}
		if operation == "is-active" {
			return unix.PidfdSendSignal(fd, 0, nil, 0)
		}
		return fmt.Errorf("unsupported sshd process operation")
	}, nil
}
