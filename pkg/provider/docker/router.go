package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) resolveAttachmentDevice(ctx context.Context, handle substrate.NodeHandle, req driver.AttachmentRequest, result driver.AttachmentResult) (string, error) {
	if result.GuestDevice != "" {
		return result.GuestDevice, nil
	}
	if len(req.IPPrefixes) == 0 {
		return "", fmt.Errorf("attachment %q has no observed device or IP", req.Name)
	}
	ip := strings.SplitN(req.IPPrefixes[0], "/", 2)[0]
	command := fmt.Sprintf(`ip -o addr show | awk '$4 ~ /^%s\// {print $2; exit}'`, ip)
	resolved, err := s.ExecInNode(ctx, handle, substrate.ExecRequest{Program: command, Shell: substrate.ShellLinux})
	if err != nil {
		return "", fmt.Errorf("resolve attachment %q: %w", req.Name, err)
	}
	device := strings.TrimSpace(resolved.Stdout)
	if device == "" {
		return "", fmt.Errorf("resolve attachment %q: no interface has IP %s", req.Name, ip)
	}
	return device, nil
}
