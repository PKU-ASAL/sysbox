package docker

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// AllowEgress inserts DOCKER-USER ACCEPT rules so containers on a NAT subnet
// can reach the internet over TCP 80/443. Some hosts (e.g. campus networks)
// DROP outbound HTTP in DOCKER-USER; the rules are inserted at the head of the
// chain so they take precedence over any DROP.
func (s *Substrate) AllowEgress(ctx context.Context, cidr string) error {
	return dockerUserRules(ctx, cidr, "-I")
}

// RemoveEgress removes the DOCKER-USER ACCEPT rules previously inserted by
// AllowEgress.
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
