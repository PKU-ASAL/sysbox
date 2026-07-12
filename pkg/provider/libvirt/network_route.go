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
	return connection.ExecInline(ctx, []string{fmt.Sprintf("ip route replace %s via %s", destination, via)})
}

func (s *Substrate) HasRoute(ctx context.Context, handle substrate.NodeHandle, destination, via string) (bool, error) {
	connection, err := s.Connection(handle, nil)
	if err != nil {
		return false, err
	}
	err = connection.ExecStream(ctx, []string{fmt.Sprintf("ip route show %s | grep -F %q", destination, "via "+via)}, io.Discard, io.Discard)
	return err == nil, err
}
