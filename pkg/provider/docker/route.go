package docker

import (
	"context"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

const routeProbeTimeout = 5 * time.Second

func (s *Substrate) EnsureRoute(ctx context.Context, handle substrate.NodeHandle, dst, via string) error {
	result, err := s.ExecInNode(ctx, handle, substrate.ExecRequest{Program: "ip", Args: []string{"route", "replace", dst, "via", via}, Shell: substrate.ShellNone})
	if err == nil && result.ExitCode != 0 {
		return fmt.Errorf("ip route replace exited %d: %s", result.ExitCode, result.Stderr)
	}
	return err
}

func (s *Substrate) HasRoute(ctx context.Context, handle substrate.NodeHandle, dst, via string) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, routeProbeTimeout)
	defer cancel()
	result, err := s.ExecInNode(probeCtx, handle, substrate.ExecRequest{Program: fmt.Sprintf("ip route show %s | grep -F %s", util.ShellQuote(dst), util.ShellQuote("via "+via)), Shell: substrate.ShellLinux})
	return err == nil && result.ExitCode == 0, err
}
