package main

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/sshaccess"
	"github.com/oslab/sysbox/pkg/substrate"
	"io"
	"net"
	"strconv"
)

type sshKeySource interface {
	SSHAuthorizedKeys(context.Context, string, string, int) ([]byte, error)
}

// Service enrollment is an administrator operation. Its profile is read from
// a trusted local file; neither worker RPCs nor access mutations choose paths,
// executables, systemd units or the configuration classes to validate.
func manageSSHService(ctx context.Context, operation string, args []string) error {
	if len(args) > 1 || operation != "install" && operation != "recover" {
		return fmt.Errorf("SSH service requires install/recover and at most one trusted profile path")
	}
	path := "/etc/sysbox/sshd-service.json"
	if len(args) == 1 {
		path = args[0]
	}
	deployment, err := sshaccess.LoadService(path)
	if err != nil {
		return err
	}
	if operation == "recover" {
		return deployment.Recover(ctx)
	}
	configs := make([]substrate.SSHAccessConfig, len(deployment.Probes))
	for i, probe := range deployment.Probes {
		configs[i] = substrate.SSHAccessConfig{BindIP: probe.LocalAddress, Port: probe.LocalPort}
	}
	return deployment.Ensure(ctx, configs)
}

// sshd supplies %u. Its AuthorizedKeysCommand supports neither %l nor %p.
// Optional endpoint arguments must come from administrator-controlled listener
// configuration; unknown endpoints cannot authorize endpoint-restricted keys.
func writeSSHAuthorizedKeys(ctx context.Context, source sshKeySource, args []string, output io.Writer) error {
	if len(args) != 1 && len(args) != 2 && len(args) != 3 {
		return fmt.Errorf("SSH authorized keys requires user and optional endpoint")
	}
	port := uint64(0)
	if len(args) > 1 {
		var err error
		port, err = strconv.ParseUint(args[len(args)-1], 10, 16)
		if err != nil || port == 0 && len(args) != 3 {
			return fmt.Errorf("invalid SSH listener port")
		}
	}
	ip := ""
	if len(args) == 3 {
		ip = args[1]
		if ip == "-" {
			ip = ""
		} else if net.ParseIP(ip) == nil {
			return fmt.Errorf("invalid SSH listener address")
		}
	}
	keys, err := source.SSHAuthorizedKeys(ctx, args[0], ip, int(port))
	if err != nil {
		return err
	}
	_, err = output.Write(keys)
	return err
}
