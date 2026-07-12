package firecracker

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) EnsureRoute(ctx context.Context, h substrate.NodeHandle, dst, via string) error {
	_, err := s.ExecInNode(ctx, h, substrate.ExecSpec{Cmd: []string{"ip", "route", "replace", dst, "via", via}})
	return err
}
func (s *Substrate) HasRoute(ctx context.Context, h substrate.NodeHandle, dst, via string) (bool, error) {
	r, err := s.ExecInNode(ctx, h, substrate.ExecSpec{Cmd: []string{"sh", "-c", fmt.Sprintf("ip route show %s | grep -F %s", dst, "via "+via)}})
	return err == nil && r.ExitCode == 0, err
}
