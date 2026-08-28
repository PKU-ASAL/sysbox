package docker

import (
	"context"
	"fmt"
	"strings"
	"time"

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
	// Docker assigns the endpoint IP asynchronously after network connect, so
	// poll briefly for the interface to appear. TODO(nat): once end-to-end NAT
	// is confirmed working, replace this busy-wait with a netlink address
	// subscription so the NAT hot path never sleeps.
	delay := 10 * time.Millisecond
	for attempt := 0; attempt < 8; attempt++ {
		resolved, err := s.ExecInNode(ctx, handle, substrate.ExecRequest{Program: command, Shell: substrate.ShellLinux})
		if err == nil {
			if device := strings.TrimSpace(resolved.Stdout); device != "" {
				return device, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(delay):
		}
		if delay < 100*time.Millisecond {
			delay *= 2
		}
	}
	ins, _ := s.cli.ContainerInspect(ctx, handle.ID)
	status := "inspect-unavailable"
	if ins.State != nil {
		status = fmt.Sprintf("running=%v pid=%d status=%s", ins.State.Running, ins.State.Pid, ins.State.Status)
	}
	diag, _ := s.ExecInNode(ctx, handle, substrate.ExecRequest{Program: "echo '==nets=='; ls /sys/class/net 2>&1; echo '==ip=='; ip -o addr show 2>&1; echo '==cmd=='; command -v ip 2>&1", Shell: substrate.ShellLinux})
	return "", fmt.Errorf("resolve attachment %q: no interface has IP %s; container[%s]; exit=%d stdout=%q stderr=%q", req.Name, ip, status, diag.ExitCode, diag.Stdout, diag.Stderr)
}
