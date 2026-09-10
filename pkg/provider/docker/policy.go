package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"

	"github.com/docker/docker/errdefs"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"

	"github.com/oslab/sysbox/pkg/driver"
	networkprovider "github.com/oslab/sysbox/pkg/provider/network"
)

type dockerPolicyTarget struct {
	ContainerID   string              `json:"container_id"`
	Bindings      map[string]string   `json:"bindings"`
	AttachmentIPs map[string][]string `json:"attachment_ips,omitempty"`
}

func decodeDockerPolicyTarget(target driver.PolicyTarget) (dockerPolicyTarget, error) {
	var state dockerPolicyTarget
	if err := json.Unmarshal(target.State, &state); err != nil {
		return state, driver.Wrap(driver.ErrorInvalidState, "docker", "decode policy target", err)
	}
	if state.ContainerID == "" {
		return state, driver.Wrap(driver.ErrorInvalidState, "docker", "policy target container_id is required", nil)
	}
	if state.Bindings == nil {
		state.Bindings = map[string]string{}
	}
	return state, nil
}

func (s *Substrate) ApplyRuleset(ctx context.Context, target driver.PolicyTarget, spec driver.RulesetSpec) (driver.RulesetObservation, error) {
	state, err := decodeDockerPolicyTarget(target)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	pid, err := s.policyTargetPID(ctx, state.ContainerID)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	netnsPath := fmt.Sprintf("/proc/%d/ns/net", pid)

	// Resolve attachment IPs to guest devices by listing interfaces inside the
	// container netns. Interface listing uses vishvananda/netlink (which carries
	// a socket deadline) rather than the nftables netlink conn that was removed
	// from the firewall path.
	ns, err := os.Open(netnsPath)
	if err != nil {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorUnavailable, "docker", "open container network namespace", err)
	}
	defer ns.Close()
	interfaces, err := policyInterfacesInNetNSFD(int(ns.Fd()))
	if err != nil {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorUnavailable, "docker", "list policy interfaces", err)
	}
	for logical, prefixes := range state.AttachmentIPs {
		if state.Bindings[logical] != "" || len(prefixes) == 0 {
			continue
		}
		device, resolveErr := resolvePolicyDevice(prefixes[0], interfaces)
		if resolveErr != nil {
			return driver.RulesetObservation{}, driver.Wrap(driver.ErrorUnavailable, "docker", fmt.Sprintf("resolve attachment %q", logical), resolveErr)
		}
		state.Bindings[logical] = device
	}

	observation, err := networkprovider.ApplyRulesetInNetNS(ctx, netnsPath, spec, state.Bindings)
	if err != nil {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorUnavailable, "docker", "apply ruleset", err)
	}
	if fwd, err := readSysctlInNetNSFD(int(ns.Fd()), "net/ipv4/ip_forward"); err == nil && fwd != "1" {
		return driver.RulesetObservation{}, fmt.Errorf("router ip_forward=%q (expected 1)", fwd)
	}
	return observation, nil
}

type policyInterface struct {
	Name      string
	Addresses []*net.IPNet
}

func policyInterfacesInNetNSFD(fd int) ([]policyInterface, error) {
	handle, err := netlink.NewHandleAt(netns.NsHandle(fd))
	if err != nil {
		return nil, fmt.Errorf("open policy network namespace: %w", err)
	}
	defer handle.Delete()
	links, err := handle.LinkList()
	if err != nil {
		return nil, fmt.Errorf("list policy interfaces: %w", err)
	}
	interfaces := make([]policyInterface, 0, len(links))
	for _, link := range links {
		addresses, err := handle.AddrList(link, netlink.FAMILY_ALL)
		if err != nil {
			return nil, fmt.Errorf("list policy interface %s addresses: %w", link.Attrs().Name, err)
		}
		item := policyInterface{Name: link.Attrs().Name, Addresses: make([]*net.IPNet, 0, len(addresses))}
		for _, address := range addresses {
			if address.IPNet != nil {
				item.Addresses = append(item.Addresses, address.IPNet)
			}
		}
		interfaces = append(interfaces, item)
	}
	return interfaces, nil
}

func resolvePolicyDevice(prefix string, interfaces []policyInterface) (string, error) {
	address := strings.SplitN(prefix, "/", 2)[0]
	ip := net.ParseIP(address)
	if ip == nil {
		return "", fmt.Errorf("invalid attachment IP %q", address)
	}
	for _, item := range interfaces {
		for _, candidate := range item.Addresses {
			if candidate != nil && candidate.IP.Equal(ip) {
				return item.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no interface has IP %s", address)
}

// ObserveRuleset reads the ruleset back inside the container's network
// namespace via an nsenter subprocess (killable), returning the observed digest.
func (s *Substrate) ObserveRuleset(ctx context.Context, target driver.PolicyTarget, owner string) (driver.RulesetObservation, error) {
	state, err := decodeDockerPolicyTarget(target)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	pid, err := s.policyTargetPID(ctx, state.ContainerID)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	return networkprovider.ObserveRulesetInNetNS(ctx, fmt.Sprintf("/proc/%d/ns/net", pid), owner)
}

func (s *Substrate) DeleteRuleset(ctx context.Context, target driver.PolicyTarget, owner string) error {
	state, err := decodeDockerPolicyTarget(target)
	if err != nil {
		return err
	}
	pid, err := s.policyTargetPID(ctx, state.ContainerID)
	if err != nil {
		return err
	}
	return networkprovider.DeleteRulesetInNetNS(ctx, fmt.Sprintf("/proc/%d/ns/net", pid), owner)
}

// policyTargetPID returns the host PID of a policy target container, mapping a
// missing container to ErrorNotFound so callers can treat it as drift.
func (s *Substrate) policyTargetPID(ctx context.Context, containerID string) (int, error) {
	container, err := s.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		category := driver.ErrorUnavailable
		if errdefs.IsNotFound(err) {
			category = driver.ErrorNotFound
		}
		return 0, driver.Wrap(category, "docker", "inspect policy target", err)
	}
	if container.State == nil || container.State.Pid == 0 {
		return 0, fmt.Errorf("policy target container %s is not running", containerID)
	}
	return container.State.Pid, nil
}

func readSysctlInNetNSFD(fd int, name string) (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	orig, err := netns.Get()
	if err != nil {
		return "", err
	}
	defer orig.Close()

	if err := netns.Set(netns.NsHandle(fd)); err != nil {
		return "", err
	}
	defer func() { _ = netns.Set(orig) }()

	data, err := os.ReadFile("/proc/sys/" + name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
