package libvirt

import (
	"context"
	"fmt"
	"io"

	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) EnsureRoute(ctx context.Context, handle substrate.NodeHandle, destination, via string) error {
	connection, err := s.Connection(handle, nil)
	if err != nil {
		return err
	}
	result, err := connection.Exec(ctx, substrate.ExecRequest{Program: "ip", Args: []string{"route", "replace", destination, "via", via}, Shell: substrate.ShellNone}, io.Discard, io.Discard)
	if err == nil && result.ExitCode != 0 {
		return fmt.Errorf("ip route replace exited %d", result.ExitCode)
	}
	return err
}

func (s *Substrate) HasRoute(ctx context.Context, handle substrate.NodeHandle, destination, via string) (bool, error) {
	connection, err := s.Connection(handle, nil)
	if err != nil {
		return false, err
	}
	result, err := connection.Exec(ctx, substrate.ExecRequest{Program: fmt.Sprintf("ip route show %s | grep -F %q", destination, "via "+via), Shell: substrate.ShellLinux}, io.Discard, io.Discard)
	return err == nil && result.ExitCode == 0, err
}
