package libvirt

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/oslab/sysbox/pkg/driver"
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
	background, ok := conn.(substrate.ConnectionBackground)
	if !ok {
		return 0, substrate.ErrNotSupported
	}
	return background.ExecBackground(ctx, req)
}

// CopyToNode copies a file through SSH and atomically installs it at dst.
func (s *Substrate) CopyToNode(ctx context.Context, handle substrate.NodeHandle, src, dst string, mode uint32) error {
	conn, err := s.Connection(handle, nil)
	if err != nil {
		return err
	}
	tmp := dst + ".sysbox-tmp"
	files, ok := conn.(substrate.ConnectionFileCopier)
	if !ok {
		return substrate.ErrNotSupported
	}
	if err := files.CopyFile(ctx, src, tmp); err != nil {
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
	knownHosts := ""
	insecureHost := false
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
		knownHosts = h.KnownHosts
		insecureHost = h.InsecureSkipHostKeyCheck
	}

	if host == "" {
		return nil, fmt.Errorf("libvirt: no SSH IP for %s; set ssh_ip in provider block or use a provisioner to configure it", handle.ID)
	}

	namespace := ""
	if len(hs.Bridges) > 0 {
		namespace = hs.Bridges[0].Netns
	}
	conn := transport.NewSSHConnectionInNamespaceWithTrust(namespace, host, port, user, key, pass, knownHosts, insecureHost)
	conn.BindExecutionIdentity(handle.ID, handle.Generation)
	return &domainConnection{ssh: conn, handle: handle}, nil
}

// Keep the gate on the connection itself: runtime callers use these SPIs
// directly, without passing through ExecInNode or CopyToNode.
type domainConnection struct {
	ssh    *transport.SSHConnection
	handle substrate.NodeHandle
}

func (c *domainConnection) validate(ctx context.Context) error {
	_, exists, err := existingDomainUUID(ctx, c.handle)
	if err != nil {
		return err
	}
	if !exists {
		return driver.Wrap(driver.ErrorNotFound, subName, "domain absent", nil)
	}
	return nil
}

func (c *domainConnection) Exec(ctx context.Context, req substrate.ExecRequest, stdout, stderr io.Writer) (result substrate.ExecResult, err error) {
	ctx, cancel := req.Context(ctx)
	defer cancel()
	defer func() { result, err = substrate.FinishExecution(ctx, result, err) }()
	if err := c.validate(ctx); err != nil {
		return result, err
	}
	return c.ssh.Exec(ctx, req, stdout, stderr)
}

func (c *domainConnection) Start(ctx context.Context, req substrate.ExecRequest) (substrate.ExecutionHandle, error) {
	ctx, cancel := req.Context(ctx)
	defer cancel()
	if err := c.validate(ctx); err != nil {
		return substrate.ExecutionHandle{}, err
	}
	return c.ssh.Start(ctx, req)
}

func (c *domainConnection) ExecBackground(ctx context.Context, req substrate.ExecRequest) (int, error) {
	handle, err := c.Start(ctx, req)
	return handle.PID, err
}

func (c *domainConnection) CopyFile(ctx context.Context, src, dst string) error {
	if err := c.validate(ctx); err != nil {
		return err
	}
	return c.ssh.CopyFile(ctx, src, dst)
}

func (c *domainConnection) WaitReady(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := c.validate(ctx); err != nil {
		return err
	}
	return c.ssh.WaitReady(ctx, timeout)
}

func (s *Substrate) OpenConsole(ctx context.Context, handle substrate.NodeHandle, req substrate.ConsoleRequest) (substrate.ConsoleSession, error) {
	conn, err := s.Connection(handle, nil)
	if err != nil {
		return nil, err
	}
	guarded, ok := conn.(*domainConnection)
	if !ok {
		return nil, substrate.ErrNotSupported
	}
	if err := guarded.validate(ctx); err != nil {
		return nil, err
	}
	return guarded.ssh.OpenConsole(ctx, transport.ConsoleRequest{
		Cmd:   req.Cmd,
		Shell: req.Shell,
		Env:   req.Env,
		Cols:  req.Cols,
		Rows:  req.Rows,
	})
}

var _ substrate.ConsoleProvider = (*Substrate)(nil)
