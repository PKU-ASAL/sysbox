package docker

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"

	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) AllowEgress(ctx context.Context, cidr string) error {
	return dockerUserRules(ctx, cidr, "-I")
}
func (s *Substrate) RemoveEgress(ctx context.Context, cidr string) error {
	return dockerUserRules(ctx, cidr, "-D")
}
func dockerUserRules(ctx context.Context, cidr, action string) error {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return err
	}
	for _, port := range []string{"80", "443"} {
		args := []string{action, "DOCKER-USER"}
		if action == "-I" {
			args = append(args, "1")
		}
		args = append(args, "-p", "tcp", "--dport", port, "-s", network.String(), "-j", "ACCEPT")
		if output, err := exec.CommandContext(ctx, "iptables", args...).CombinedOutput(); err != nil && action != "-D" {
			return fmt.Errorf("iptables %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
		}
	}
	return nil
}

func (s *Substrate) EnsureRoute(ctx context.Context, handle substrate.NodeHandle, dst, via string) error {
	_, err := s.ExecInNode(ctx, handle, substrate.ExecSpec{Cmd: []string{"ip", "route", "replace", dst, "via", via}})
	return err
}
func (s *Substrate) HasRoute(ctx context.Context, handle substrate.NodeHandle, dst, via string) (bool, error) {
	result, err := s.ExecInNode(ctx, handle, substrate.ExecSpec{Cmd: []string{"sh", "-c", fmt.Sprintf("ip route show %s | grep -F %s", dst, "via "+via)}})
	return err == nil && result.ExitCode == 0, err
}
