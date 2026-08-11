package firecracker

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) EnsureRoute(ctx context.Context, h substrate.NodeHandle, dst, via string) error {
	result, err := s.ExecInNode(ctx, h, substrate.ExecRequest{Program: "ip", Args: []string{"route", "replace", dst, "via", via}, Shell: substrate.ShellNone})
	if err == nil && result.ExitCode != 0 {
		return fmt.Errorf("ip route replace exited %d: %s", result.ExitCode, result.Stderr)
	}
	return err
}
func (s *Substrate) HasRoute(ctx context.Context, h substrate.NodeHandle, dst, via string) (bool, error) {
	r, err := s.ExecInNode(ctx, h, substrate.ExecRequest{Program: fmt.Sprintf("ip route show %s | grep -F %s", dst, "via "+via), Shell: substrate.ShellLinux})
	return err == nil && r.ExitCode == 0, err
}
