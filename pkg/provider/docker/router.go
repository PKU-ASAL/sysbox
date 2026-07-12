package docker

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) ConfigureNAT(ctx context.Context, handle substrate.NodeHandle, fromIf, toIf string) error {
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
