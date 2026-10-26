package libvirt

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
)

var (
	guestNetworkReadyTimeout = 2 * time.Minute
	guestNetworkPollInterval = time.Second
	guestNetworkProbe        = probeGuestIPv4
)

func (s *Substrate) PrepareGuestNetwork(ctx context.Context, handle substrate.NodeHandle) error {
	hs := hsFrom(handle)
	switch hs.NetworkInit {
	case substrate.GuestNetworkInitCloudInit:
		if err := ensureCloudInitSSHAccess(ctx, hs); err != nil {
			return err
		}
		seedISO, err := createNoCloudSeed(hs.VMDir, hs.DomainName, hs.Bridges, hs.SSHUser, hs.SSHAuthorizedKey)
		if err != nil {
			return err
		}
		hs.SeedISO = seedISO
		return nil
	case substrate.GuestNetworkInitPreconfigured:
		hs.SeedISO = ""
		return nil
	default:
		return fmt.Errorf("libvirt: unsupported guest network initialization mode %q", hs.NetworkInit)
	}
}

func (s *Substrate) ObserveGuestNetwork(ctx context.Context, handle substrate.NodeHandle) (substrate.GuestNetworkInitObservation, error) {
	hs := hsFrom(handle)
	observation := substrate.GuestNetworkInitObservation{Mode: hs.NetworkInit, Converged: len(hs.Bridges) == 0}
	var managedTargets []network.BridgeTarget
	for _, bridge := range hs.Bridges {
		if bridge.Netns == "" {
			if bridge.Target.Managed == nil || bridge.Target.Bridge != bridge.Bridge {
				return observation, fmt.Errorf("libvirt attachment %s: missing namespace or managed root bridge proof", bridge.Name)
			}
			managedTargets = append(managedTargets, bridge.Target)
		}
		for _, prefix := range bridge.IPPrefixes {
			ip, _, err := net.ParseCIDR(prefix)
			if err != nil {
				return observation, fmt.Errorf("libvirt attachment %s: invalid address prefix %q: %w", bridge.Name, prefix, err)
			}
			if ip.To4() == nil {
				return observation, fmt.Errorf("libvirt attachment %s: IPv6 address %q is not supported", bridge.Name, prefix)
			}
		}
	}

	if len(managedTargets) > 0 {
		_, unlock, err := network.LockBridgeTargets(ctx, managedTargets)
		if err != nil {
			return observation, err
		}
		defer unlock()
	}
	deadline := time.Now().Add(guestNetworkReadyTimeout)
	for {
		observation.Interfaces = observation.Interfaces[:0]
		observation.Converged = true
		observation.Reason = ""
		for _, bridge := range hs.Bridges {
			item := substrate.GuestNetworkInterfaceObservation{Name: bridge.Name, MAC: bridge.MAC, IPPrefixes: append([]string(nil), bridge.IPPrefixes...), Converged: true}
			for _, prefix := range bridge.IPPrefixes {
				ip := strings.SplitN(prefix, "/", 2)[0]
				if err := guestNetworkProbe(ctx, bridge.Netns, ip); err != nil {
					item.Converged = false
					item.Reason = fmt.Sprintf("%s from namespace %s: %v", ip, bridge.Netns, err)
					observation.Converged = false
					observation.Reason = item.Reason
					break
				}
			}
			observation.Interfaces = append(observation.Interfaces, item)
		}
		if observation.Converged || time.Now().After(deadline) {
			return observation, nil
		}
		select {
		case <-ctx.Done():
			return observation, ctx.Err()
		case <-time.After(guestNetworkPollInterval):
		}
	}
}

func probeGuestIPv4(ctx context.Context, namespace, ip string) error {
	program, args := "ping", []string{"-c", "1", "-W", "1", ip}
	if namespace != "" {
		program = "ip"
		args = append([]string{"netns", "exec", namespace, "ping"}, args...)
	}
	output, err := exec.CommandContext(ctx, program, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ping: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}
