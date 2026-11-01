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
	if req.CaptureViaFile && hsFrom(handle).GuestFamily != substrate.GuestFamilyWindows {
		return substrate.ExecResult{}, fmt.Errorf("libvirt: file capture requires Windows")
	}
	conn, err := s.Connection(handle, nil)
	if err != nil {
		return substrate.ExecResult{}, err
	}
	return conn.Exec(ctx, req, io.Discard, io.Discard)
}

// CopyToNode copies a file through SSH and atomically installs it at dst.
func (s *Substrate) CopyToNode(ctx context.Context, handle substrate.NodeHandle, src, dst string, mode uint32) error {
	conn, err := s.Connection(handle, nil)
	if err != nil {
		return err
	}
	if hsFrom(handle).GuestFamily == substrate.GuestFamilyWindows {
		copier, ok := conn.(substrate.ConnectionFileCopier)
		if !ok {
			return substrate.ErrNotSupported
		}
		return copier.CopyFile(ctx, src, dst)
	}
	return transport.InstallFile(ctx, conn, src, dst, mode)
}

// Connection returns an SSH connection to the VM. The SSHIP is resolved from
// the declared attachment or discovered post-boot.
// If SSHIP is empty, provisioners cannot run via SSH.
func (s *Substrate) Connection(handle substrate.NodeHandle, hints []substrate.ConnectionHint) (substrate.Connection, error) {
	hs := hsFrom(handle)
	if hs.GuestFamily == substrate.GuestFamilyWindows {
		return &qgaDomainConnection{handle: handle, qga: &qgaConnection{domain: hs.DomainUUID}}, nil
	}

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

func (c *domainConnection) ControlExecution(ctx context.Context, h substrate.ExecutionHandle, control substrate.TerminalControl) error {
	if err := c.validate(ctx); err != nil {
		return err
	}
	return c.ssh.ControlExecution(ctx, h, control)
}

func (c *domainConnection) ObserveExecution(ctx context.Context, h substrate.ExecutionHandle) (substrate.ExecResult, error) {
	if err := c.validate(ctx); err != nil {
		return substrate.ExecResult{}, err
	}
	return c.ssh.ObserveExecution(ctx, h)
}
func (c *domainConnection) CancelExecution(ctx context.Context, h substrate.ExecutionHandle) (substrate.RemoteTermination, error) {
	if err := c.validate(ctx); err != nil {
		return substrate.RemoteTerminationUnknown, err
	}
	return c.ssh.CancelExecution(ctx, h)
}
func (c *domainConnection) ReadExecutionOutput(ctx context.Context, h substrate.ExecutionHandle, s string, p int64, n int) (substrate.ExecutionOutput, error) {
	if err := c.validate(ctx); err != nil {
		return substrate.ExecutionOutput{}, err
	}
	return c.ssh.ReadExecutionOutput(ctx, h, s, p, n)
}
func (c *domainConnection) ListExecutions(ctx context.Context) ([]substrate.ExecutionHandle, error) {
	if err := c.validate(ctx); err != nil {
		return nil, err
	}
	return c.ssh.ListExecutions(ctx)
}
func (c *domainConnection) RenewExecution(ctx context.Context, h substrate.ExecutionHandle) error {
	if err := c.validate(ctx); err != nil {
		return err
	}
	return c.ssh.RenewExecution(ctx, h)
}
func (c *domainConnection) GuestTaskBootID(ctx context.Context) (string, error) {
	if err := c.validate(ctx); err != nil {
		return "", err
	}
	return c.ssh.GuestTaskBootID(ctx)
}
func (c *domainConnection) CopyFile(ctx context.Context, src, dst string) error {
	if err := c.validate(ctx); err != nil {
		return err
	}
	return c.ssh.CopyFile(ctx, src, dst)
}

func (c *domainConnection) DiscoverSSHAccess(ctx context.Context, q substrate.SSHAccessQuery) (substrate.SSHAccessDiscovery, error) {
	if err := c.validate(ctx); err != nil {
		return substrate.SSHAccessDiscovery{}, err
	}
	return c.ssh.DiscoverSSHAccess(ctx, q)
}
func (c *domainConnection) MutateSSHAccess(ctx context.Context, req substrate.SSHAccessMutation) (substrate.SSHAccessResult, error) {
	if err := c.validate(ctx); err != nil {
		return substrate.SSHAccessResult{}, err
	}
	return c.ssh.MutateSSHAccess(ctx, req)
}
func (c *domainConnection) ObserveSSHAccess(ctx context.Context, handle substrate.SSHAccessHandle) (substrate.SSHAccessResult, error) {
	if err := c.validate(ctx); err != nil {
		return substrate.SSHAccessResult{}, err
	}
	return c.ssh.ObserveSSHAccess(ctx, handle)
}
func (c *domainConnection) InspectSSHAccess(ctx context.Context, handle substrate.SSHAccessHandle) (substrate.SSHAccessInspection, error) {
	if err := c.validate(ctx); err != nil {
		return substrate.SSHAccessInspection{}, err
	}
	return c.ssh.InspectSSHAccess(ctx, handle)
}

var _ substrate.ConnectionSSHAccess = (*domainConnection)(nil)

func (c *domainConnection) WaitReady(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := c.validate(ctx); err != nil {
		return err
	}
	return c.ssh.WaitReady(ctx, timeout)
}

// QGA calls always address the immutable domain UUID and validate its binding.
// Durable operations require the installed, scope-bound Windows task helper.
// Missing or untrusted helpers fail closed; raw Exec is never their fallback.
type qgaDomainConnection struct {
	handle substrate.NodeHandle
	qga    *qgaConnection
}

func (c *qgaDomainConnection) validate(ctx context.Context) error {
	return (&domainConnection{handle: c.handle}).validate(ctx)
}
func (c *qgaDomainConnection) Exec(ctx context.Context, req substrate.ExecRequest, out, errOut io.Writer) (substrate.ExecResult, error) {
	ctx, cancel := req.Context(ctx)
	defer cancel()
	if err := c.validate(ctx); err != nil {
		return substrate.ExecResult{}, err
	}
	return c.qga.Exec(ctx, req, out, errOut)
}
func (c *qgaDomainConnection) CopyFile(ctx context.Context, src, dst string) error {
	if err := c.validate(ctx); err != nil {
		return err
	}
	return c.qga.CopyFile(ctx, src, dst)
}
func (c *qgaDomainConnection) WaitReady(ctx context.Context, timeout time.Duration) error {
	if err := c.validate(ctx); err != nil {
		return err
	}
	return c.qga.WaitReady(ctx, timeout)
}
