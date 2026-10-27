package docker

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"

	"github.com/oslab/sysbox/pkg/driver"
	ownednetwork "github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
)

// CreateBridgeNetwork creates a Docker-managed bridge network with NAT.
// Docker's bridge driver automatically sets up iptables MASQUERADE for outbound
// traffic, giving attached containers internet access via the host's default route.
// Returns the Docker network ID.
//
// Idempotent: if a network with the same name already exists (leftover from
// a failed apply), its ID is returned instead of failing with a name conflict.
func (s *Substrate) CreateBridgeNetwork(ctx context.Context, name, cidr string, labels map[string]string) (string, error) {
	return s.createBridgeNetwork(ctx, name, cidr, labels, "")
}

func (s *Substrate) createBridgeNetwork(ctx context.Context, name, cidr string, labels map[string]string, bridge string) (string, error) {
	expected, err := expectedLabels(ctx, labels, false)
	if err != nil {
		return "", err
	}
	if existing, err := s.cli.NetworkInspect(ctx, name, network.InspectOptions{}); err == nil {
		if len(expected) == 0 {
			return "", driver.Wrap(driver.ErrorInvalidState, "docker", "network exists without expected ownership", nil)
		}
		if err := verifyLabels(existing.Labels, expected); err != nil {
			return "", err
		}
		return existing.ID, nil
	} else if !errdefs.IsNotFound(err) {
		return "", driver.Wrap(driver.ErrorUnavailable, "docker", "network inspection unavailable", err)
	}
	labels, err = expectedLabels(ctx, labels, true)
	if err != nil {
		return "", err
	}

	// Parse gateway (first usable host in the subnet).
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf("parse cidr %s: %w", cidr, err)
	}
	gw := firstHost(ipNet)

	options := map[string]string{"com.docker.network.bridge.enable_ip_masquerade": "true", "com.docker.network.bridge.enable_icc": "true"}
	if bridge != "" {
		options["com.docker.network.bridge.name"] = bridge
	}
	resp, err := s.cli.NetworkCreate(ctx, name, network.CreateOptions{
		Driver: "bridge",
		Labels: labels,
		IPAM: &network.IPAM{
			Driver: "default",
			Config: []network.IPAMConfig{
				{Subnet: cidr, Gateway: gw},
			},
		},
		Options: options,
	})
	if err != nil {
		return "", fmt.Errorf("create docker bridge network %s: %w", name, err)
	}
	return resp.ID, nil
}

// RemoveBridgeNetwork removes a Docker-managed bridge network by ID.
// Idempotent: an already-absent network is not an error.
func (s *Substrate) RemoveBridgeNetwork(ctx context.Context, networkID string) error {
	err := s.cli.NetworkRemove(ctx, networkID)
	if errdefs.IsNotFound(err) {
		return nil
	}
	return err
}

// CreateManagedNetwork implements substrate.Substrate by creating a Docker
// bridge network. Currently always NAT (NAT field is informational).
func (s *Substrate) CreateManagedNetwork(ctx context.Context, spec substrate.ManagedNetworkSpec) (substrate.ManagedNetworkInfo, error) {
	if spec.ExposeBridge {
		return s.createExposedManagedNetwork(ctx, spec)
	}
	netName := fmt.Sprintf("sysbox-nat-%s", spec.Name)
	id, err := s.CreateBridgeNetwork(ctx, netName, spec.CIDR, spec.Labels)
	if err != nil {
		return substrate.ManagedNetworkInfo{}, err
	}
	return substrate.ManagedNetworkInfo{ID: id, Name: netName}, nil
}

// RemoveManagedNetwork implements substrate.Substrate.
func (s *Substrate) RemoveManagedNetwork(ctx context.Context, id string) error {
	return s.removeManagedNetwork(ctx, id)
}

// ReadManagedNetwork queries an existing Docker network by name without
// creating one. Tries the raw name first, then the sysbox-prefixed variant.
func (s *Substrate) ReadManagedNetwork(ctx context.Context, spec substrate.ManagedNetworkSpec) (substrate.ManagedNetworkInfo, error) {
	expected, err := expectedLabels(ctx, spec.Labels, false)
	if err != nil {
		return substrate.ManagedNetworkInfo{}, err
	}
	names := []string{spec.Name}
	// Discovery receives a logical name and expected labels. Reads by persisted
	// network ID have no labels and must never switch to a different resource.
	if len(spec.Labels) > 0 && !strings.HasPrefix(spec.Name, "sysbox-nat-") {
		names = append(names, "sysbox-nat-"+spec.Name)
	}
	for _, name := range names {
		existing, err := s.cli.NetworkInspect(ctx, name, network.InspectOptions{})
		if err != nil {
			if errdefs.IsNotFound(err) {
				continue
			}
			return substrate.ManagedNetworkInfo{}, driver.Wrap(driver.ErrorUnavailable, "docker", "network inspection unavailable", err)
		}
		if err := verifyLabels(existing.Labels, expected); err != nil {
			return substrate.ManagedNetworkInfo{}, err
		}
		info := substrate.ManagedNetworkInfo{ID: existing.ID, Name: existing.Name}
		if spec.ExposeBridge || existing.Labels[managedBridgeLabel] == "v1" {
			proof, err := ownednetwork.LoadManagedBridge("docker", existing.ID)
			if err != nil {
				return substrate.ManagedNetworkInfo{}, err
			}
			if err := s.VerifyManagedBridge(ctx, *proof); err != nil {
				return substrate.ManagedNetworkInfo{}, err
			}
			if err := ownednetwork.InspectManagedBridge(ctx, *proof); err != nil {
				return substrate.ManagedNetworkInfo{}, err
			}
			info.BridgeTarget = proof
		}
		return info, nil
	}
	return substrate.ManagedNetworkInfo{}, driver.Wrap(driver.ErrorNotFound, "docker", "managed network absent", nil)
}

// ConnectContainerToNetwork attaches a running container to a Docker network
// with a static IP address. Used for nat=true networks.
func (s *Substrate) ConnectContainerToNetwork(ctx context.Context, containerID, networkID, ip, mac string, aliases []string) error {
	settings, err := dockerEndpointSettings(ip, mac, aliases)
	if err != nil {
		return err
	}
	return s.cli.NetworkConnect(ctx, networkID, containerID, settings)
}

func dockerEndpointSettings(ip, mac string, aliases []string) (*network.EndpointSettings, error) {
	// Strip prefix length for Docker API (it wants bare IP, not CIDR).
	host, _, err := net.ParseCIDR(ip)
	if err != nil {
		// ip may already be a bare address.
		host = net.ParseIP(ip)
		if host == nil {
			return nil, fmt.Errorf("invalid IP address %q", ip)
		}
	}

	return &network.EndpointSettings{
		Aliases:    append([]string(nil), aliases...),
		MacAddress: mac,
		IPAMConfig: &network.EndpointIPAMConfig{
			IPv4Address: host.String(),
		},
	}, nil
}

// firstHost returns the first usable host IP in a subnet as a string.
// e.g. 172.20.0.0/24 → "172.20.0.1"
func firstHost(n *net.IPNet) string {
	ip := make(net.IP, len(n.IP))
	copy(ip, n.IP)
	ip[len(ip)-1]++
	return ip.String()
}
