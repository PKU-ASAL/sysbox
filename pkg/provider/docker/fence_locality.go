package docker

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/substrate"
)

// HostFenceScope is meaningful only inside an independently enrolled disposable
// host whose local sockets/storage are controlled by the acceptance controller.
func (s *Substrate) HostFenceScope(context.Context) (string, error) {
	if s == nil || s.cli == nil || s.cli.DaemonHost() != "unix:///var/run/docker.sock" {
		return "", fmt.Errorf("host fencing requires the local default Docker socket")
	}
	return "local-only", nil
}

func (s *Substrate) HostFenceDeletionAbsent(ctx context.Context, h substrate.NodeHandle) error {
	if _, err := s.HostFenceScope(ctx); err != nil {
		return err
	}
	if h.ID == "" {
		return fmt.Errorf("missing exact container identity")
	}
	observed, err := s.ObserveNode(ctx, h)
	if err != nil {
		return err
	}
	if observed.Status != substrate.NodeStatusMissing {
		return fmt.Errorf("container absence not proven")
	}
	return nil
}
