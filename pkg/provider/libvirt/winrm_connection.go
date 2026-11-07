package libvirt

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
)

// WinRM is selected only for foreground commands. The embedded QGA connection
// preserves the existing signed helper for durable tasks and file operations;
// failures never cause either operation to be retried on the other channel.
type winRMDomainConnection struct {
	*qgaDomainConnection
	foreground *transport.WinRMConnection
}

func newWinRMDomainConnection(handle substrate.NodeHandle, hint substrate.ConnectionHint) (substrate.Connection, error) {
	hs := hsFrom(handle)
	if hs.GuestFamily != substrate.GuestFamilyWindows {
		return nil, fmt.Errorf("WinRM requires a Windows libvirt node")
	}
	if hint.InsecureSkipHostKeyCheck || hint.KnownHosts != "" || hint.PrivateKey != "" {
		return nil, fmt.Errorf("WinRM uses system TLS trust; SSH trust overrides are not supported")
	}
	for _, bridge := range hs.Bridges {
		if bridge.Netns != "" {
			return nil, fmt.Errorf("WinRM does not support an isolated network namespace; use QGA")
		}
	}
	endpoint, err := url.Parse(hint.Host)
	if err != nil {
		return nil, fmt.Errorf("invalid WinRM endpoint")
	}
	// No DNS aliases or arbitrary management URLs: the TLS IP SAN must match
	// the declared managed guest address. Domain identity is checked per call.
	target := net.ParseIP(endpoint.Hostname())
	if target == nil || target.IsLoopback() || target.IsUnspecified() || target.IsMulticast() {
		return nil, fmt.Errorf("WinRM endpoint must name the managed guest IP")
	}
	matches := false
	for _, candidate := range []string{handle.Net.PrimaryIP} {
		if ip := net.ParseIP(candidate); ip != nil && ip.Equal(target) {
			matches = true
		}
	}
	for _, bridge := range hs.Bridges {
		for _, prefix := range bridge.IPPrefixes {
			ip, _, err := net.ParseCIDR(prefix)
			if err == nil && ip.Equal(target) {
				matches = true
			}
		}
	}
	if !matches {
		return nil, fmt.Errorf("WinRM endpoint does not match managed guest addresses")
	}
	foreground, err := transport.NewWinRMConnection(hint.Host, hint.User, hint.Password)
	if err != nil {
		return nil, err
	}
	return &winRMDomainConnection{qgaDomainConnection: &qgaDomainConnection{handle: handle, qga: &qgaConnection{domain: hs.DomainUUID}}, foreground: foreground}, nil
}
func (c *winRMDomainConnection) Exec(ctx context.Context, req substrate.ExecRequest, out, errOut io.Writer) (result substrate.ExecResult, err error) {
	ctx, cancel := req.Context(ctx)
	defer cancel()
	ctx, bound := context.WithTimeout(ctx, 5*time.Minute)
	defer bound()
	defer func() { result, err = substrate.FinishExecution(ctx, result, err) }()
	if err := req.Validate(); err != nil {
		return result, err
	}
	if err := c.validate(ctx); err != nil {
		return substrate.ExecResult{}, err
	}
	return c.foreground.Exec(ctx, req, out, errOut)
}
func (c *winRMDomainConnection) WaitReady(ctx context.Context, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := c.validate(ctx); err != nil {
		return err
	}
	return c.foreground.WaitReady(ctx, timeout)
}
