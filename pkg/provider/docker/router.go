package docker

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) ConfigureNAT(ctx context.Context, handle substrate.NodeHandle, fromReq driver.AttachmentRequest, from driver.AttachmentResult, toReq driver.AttachmentRequest, to driver.AttachmentResult) error {
	fromIf, err := s.resolveAttachmentDevice(ctx, handle, fromReq, from)
	if err != nil {
		return err
	}
	toIf, err := s.resolveAttachmentDevice(ctx, handle, toReq, to)
	if err != nil {
		return err
	}
	container, err := s.cli.ContainerInspect(ctx, handle.ID)
	if err != nil {
		return fmt.Errorf("inspect router %s: %w", handle.ID, err)
	}
	if container.State == nil || container.State.Pid == 0 {
		return fmt.Errorf("router %s is not running", handle.ID)
	}
	pid := strconv.Itoa(container.State.Pid)
	commands := [][]string{
		{"-t", pid, "-n", "iptables", "-t", "nat", "-A", "POSTROUTING", "-o", toIf, "-j", "MASQUERADE"},
		{"-t", pid, "-n", "iptables", "-A", "FORWARD", "-i", fromIf, "-o", toIf, "-j", "ACCEPT"},
		{"-t", pid, "-n", "iptables", "-A", "FORWARD", "-i", toIf, "-o", fromIf, "-m", "state", "--state", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
	}
	for _, args := range commands {
		if output, err := exec.CommandContext(ctx, "nsenter", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("nsenter %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
		}
	}
	return nil
}

func (s *Substrate) resolveAttachmentDevice(ctx context.Context, handle substrate.NodeHandle, req driver.AttachmentRequest, result driver.AttachmentResult) (string, error) {
	if result.GuestDevice != "" {
		return result.GuestDevice, nil
	}
	if len(req.IPPrefixes) == 0 {
		return "", fmt.Errorf("attachment %q has no observed device or IP", req.Name)
	}
	ip := strings.SplitN(req.IPPrefixes[0], "/", 2)[0]
	command := fmt.Sprintf(`ip -o addr show | awk '$4 ~ /^%s\// {print $2; exit}'`, ip)
	resolved, err := s.ExecInNode(ctx, handle, substrate.ExecSpec{Cmd: []string{"sh", "-c", command}})
	if err != nil {
		return "", fmt.Errorf("resolve attachment %q: %w", req.Name, err)
	}
	device := strings.TrimSpace(resolved.Stdout)
	if device == "" {
		return "", fmt.Errorf("resolve attachment %q: no interface has IP %s", req.Name, ip)
	}
	return device, nil
}
