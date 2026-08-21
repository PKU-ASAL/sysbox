package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
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
	var observation driver.RulesetObservation
	err = s.withContainerNetNS(ctx, state.ContainerID, func(fd int) error {
		interfaces, listErr := policyInterfacesInNetNSFD(fd)
		if listErr != nil {
			return listErr
		}
		for logical, prefixes := range state.AttachmentIPs {
			if state.Bindings[logical] != "" || len(prefixes) == 0 {
				continue
			}
			device, resolveErr := resolvePolicyDevice(prefixes[0], interfaces)
			if resolveErr != nil {
				return fmt.Errorf("resolve attachment %q: %w", logical, resolveErr)
			}
			state.Bindings[logical] = device
		}
		var applyErr error
		observation, applyErr = networkprovider.ApplyRulesetInNetNSFD(fd, spec, state.Bindings)
		return applyErr
	})
	if err != nil {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorUnavailable, "docker", "apply ruleset", err)
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

func (s *Substrate) ObserveRuleset(ctx context.Context, target driver.PolicyTarget, owner string) (driver.RulesetObservation, error) {
	state, err := decodeDockerPolicyTarget(target)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	var observation driver.RulesetObservation
	err = s.withContainerNetNS(ctx, state.ContainerID, func(fd int) error {
		var observeErr error
		observation, observeErr = networkprovider.ObserveRulesetInNetNSFD(fd, owner)
		return observeErr
	})
	return observation, err
}

func (s *Substrate) DeleteRuleset(ctx context.Context, target driver.PolicyTarget, owner string) error {
	state, err := decodeDockerPolicyTarget(target)
	if err != nil {
		return err
	}
	return s.withContainerNetNS(ctx, state.ContainerID, func(fd int) error {
		return networkprovider.DeleteRulesetInNetNSFD(fd, owner)
	})
}

func (s *Substrate) withContainerNetNS(ctx context.Context, containerID string, fn func(int) error) error {
	container, err := s.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		category := driver.ErrorUnavailable
		if errdefs.IsNotFound(err) {
			category = driver.ErrorNotFound
		}
		return driver.Wrap(category, "docker", "inspect policy target", err)
	}
	if container.State == nil || container.State.Pid == 0 {
		return fmt.Errorf("policy target container %s is not running", containerID)
	}
	ns, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", container.State.Pid))
	if err != nil {
		return fmt.Errorf("open container network namespace: %w", err)
	}
	defer ns.Close()
	return fn(int(ns.Fd()))
}
