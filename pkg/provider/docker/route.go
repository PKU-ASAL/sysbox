package docker

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) EnsureRoute(ctx context.Context, handle substrate.NodeHandle, dst, via string) error {
	_, err := s.ExecInNode(ctx, handle, substrate.ExecRequest{Program: "ip", Args: []string{"route", "replace", dst, "via", via}, Shell: substrate.ShellNone})
	return err
}

func (s *Substrate) HasRoute(ctx context.Context, handle substrate.NodeHandle, dst, via string) (bool, error) {
	result, err := s.ExecInNode(ctx, handle, substrate.ExecRequest{Program: fmt.Sprintf("ip route show %s | grep -F %s", dst, "via "+via), Shell: substrate.ShellLinux})
	return err == nil && result.ExitCode == 0, err
}
