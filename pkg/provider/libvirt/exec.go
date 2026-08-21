package libvirt

import (
	"context"
	"fmt"
	"io"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
)

// ExecInNode runs a command in the VM through its declared SSH connection.
func (s *Substrate) ExecInNode(ctx context.Context, handle substrate.NodeHandle, req substrate.ExecRequest) (substrate.ExecResult, error) {
	conn, err := s.Connection(handle, nil)
	if err != nil {
		return substrate.ExecResult{}, err
	}
	return conn.Exec(ctx, req, io.Discard, io.Discard)
}

// ExecBackground starts a detached command in the VM through SSH.
func (s *Substrate) ExecBackground(ctx context.Context, handle substrate.NodeHandle, req substrate.ExecRequest) (int, error) {
	conn, err := s.Connection(handle, nil)
	if err != nil {
		return 0, err
	}
	return conn.ExecBackground(ctx, req)
}

// CopyToNode copies a file through SSH and atomically installs it at dst.
func (s *Substrate) CopyToNode(ctx context.Context, handle substrate.NodeHandle, src, dst string, mode uint32) error {
	conn, err := s.Connection(handle, nil)
	if err != nil {
		return err
	}
	tmp := dst + ".sysbox-tmp"
	if err := conn.CopyFile(ctx, src, tmp); err != nil {
		return err
	}
	result, err := conn.Exec(ctx, guestFileInstallRequest("chmod", fmt.Sprintf("%04o", mode), tmp), io.Discard, io.Discard)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("set guest file mode")
	}
	result, err = conn.Exec(ctx, guestFileInstallRequest("mv", "-f", tmp, dst), io.Discard, io.Discard)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("atomic guest file rename")
	}
	return nil
}

func guestFileInstallRequest(program string, args ...string) substrate.ExecRequest {
	return substrate.ExecRequest{Program: program, Args: args, Shell: substrate.ShellNone}
}

// Connection returns an SSH connection to the VM. The SSHIP is resolved from
// the declared attachment or discovered post-boot.
// If SSHIP is empty, provisioners cannot run via SSH.
func (s *Substrate) Connection(handle substrate.NodeHandle, hints []substrate.ConnectionHint) (substrate.Connection, error) {
	hs := hsFrom(handle)

	host := handle.Net.PrimaryIP
	if hs.SSHIP != "" {
		host = hs.SSHIP
	}
	port := "22"
	user := hs.SSHUser
	pass := hs.SSHPass
	key := hs.SSHKey
	if user == "" {
		user = "root"
	}

	// HCL connection {} hint overrides.
	if len(hints) > 0 {
		h := hints[0]
		if h.Host != "" {
			host = h.Host
		}
		if h.User != "" {
			user = h.User
		}
		if h.Password != "" {
			pass = h.Password
		}
		if h.PrivateKey != "" {
			key = h.PrivateKey
		}
	}

	if host == "" {
		return nil, fmt.Errorf("libvirt: no SSH IP for %s; set ssh_ip in provider block or use a provisioner to configure it", handle.ID)
	}

	namespace := ""
	if len(hs.Bridges) > 0 {
		namespace = hs.Bridges[0].Netns
	}
	if namespace != "" {
		return transport.NewSSHConnectionInNamespace(namespace, host, port, user, key, pass), nil
	}
	return transport.NewSSHConnectionWithPort(host, port, user, key, pass), nil
}

func (s *Substrate) OpenConsole(ctx context.Context, handle substrate.NodeHandle, req substrate.ConsoleRequest) (substrate.ConsoleSession, error) {
	conn, err := s.Connection(handle, nil)
	if err != nil {
		return nil, err
	}
	ssh, ok := conn.(*transport.SSHConnection)
	if !ok {
		return nil, substrate.ErrNotSupported
	}
	return ssh.OpenConsole(ctx, transport.ConsoleRequest{
		Cmd:   req.Cmd,
		Shell: req.Shell,
		Env:   req.Env,
		Cols:  req.Cols,
		Rows:  req.Rows,
	})
}

var _ substrate.ConsoleProvider = (*Substrate)(nil)
