package sshaccess

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ServiceProfile is an administrator-enrolled guest service, not an RPC payload.
// ConfigPath must be the main configuration actually used by ServiceUnit.
type ServiceProfile struct {
	Version     int         `json:"version"`
	Root        string      `json:"root"`
	ConfigPath  string      `json:"config_path"`
	DropInPath  string      `json:"drop_in_path"`
	Helper      string      `json:"helper"`
	CommandUser string      `json:"command_user"`
	SSHD        string      `json:"sshd"`
	Systemctl   string      `json:"systemctl"`
	ServiceUnit string      `json:"service_unit"`
	Probes      []SSHDProbe `json:"probes"`
	Listeners   []string    `json:"listeners"`
}

var serviceUnit = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,127}\.service$`)

func LoadService(path string) (*Deployment, error) {
	raw, err := readDeploymentFile(path)
	if err != nil {
		return nil, fmt.Errorf("trusted SSH service profile required: %w", err)
	}
	var p ServiceProfile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&p); err != nil {
		return nil, err
	}
	if dec.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("trailing SSH service profile data")
	}
	if p.Version != 1 || !serviceUnit.MatchString(p.ServiceUnit) || len(p.Listeners) == 0 || len(p.Listeners) > 128 {
		return nil, fmt.Errorf("invalid SSH service profile")
	}
	for _, binary := range []string{p.Systemctl, p.SSHD, p.Helper} {
		if !filepath.IsAbs(binary) || filepath.Clean(binary) != binary {
			return nil, fmt.Errorf("absolute trusted service executable required")
		}
		if err = trustedPath(binary, false); err != nil {
			return nil, err
		}
	}
	for _, endpoint := range p.Listeners {
		host, port, err := net.SplitHostPort(endpoint)
		portNumber, portErr := strconv.ParseUint(port, 10, 16)
		if err != nil || net.ParseIP(host) == nil || portErr != nil || portNumber == 0 {
			return nil, fmt.Errorf("explicit SSH readiness endpoint required")
		}
	}
	run, err := NewSSHDRunner(p.SSHD)
	if err != nil {
		return nil, err
	}
	control := func(ctx context.Context, operation string) error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, p.Systemctl, "--no-ask-password", operation, p.ServiceUnit)
		cmd.WaitDelay = 100 * time.Millisecond
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("SSH service %s: %w", operation, err)
		}
		return ctx.Err()
	}
	profileBytes, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	d := &Deployment{ServiceIdentity: contentHash(profileBytes), Root: p.Root, ConfigPath: p.ConfigPath, DropInPath: p.DropInPath, Helper: p.Helper, CommandUser: p.CommandUser, Probes: p.Probes, Run: run,
		Reload: func(ctx context.Context) error { return control(ctx, "reload") },
		Ready: func(ctx context.Context) error {
			if err := control(ctx, "is-active"); err != nil {
				return err
			}
			for _, endpoint := range p.Listeners {
				if err := sshListenerReady(ctx, endpoint); err != nil {
					return err
				}
			}
			return nil
		},
	}
	if _, err = d.identity(); err != nil {
		return nil, err
	}
	return d, nil
}

func sshListenerReady(ctx context.Context, endpoint string) error {
	deadline := time.Now().Add(2 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	conn, err := (&net.Dialer{Deadline: deadline}).DialContext(ctx, "tcp", endpoint)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	line, err := bufio.NewReaderSize(conn, 1024).ReadSlice('\n')
	if err != nil || len(line) > 255 || !strings.HasPrefix(string(line), "SSH-2.0-") {
		return fmt.Errorf("SSH listener %s did not provide a valid banner", endpoint)
	}
	return nil
}
