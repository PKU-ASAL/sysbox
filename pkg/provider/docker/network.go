package docker

import (
	"context"
	"fmt"
	"net"

	"github.com/docker/docker/api/types/network"

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
	if existing, err := s.cli.NetworkInspect(ctx, name, network.InspectOptions{}); err == nil {
		return existing.ID, nil
	}

	// Parse gateway (first usable host in the subnet).
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf("parse cidr %s: %w", cidr, err)
	}
	gw := firstHost(ipNet)

	resp, err := s.cli.NetworkCreate(ctx, name, network.CreateOptions{
		Driver: "bridge",
		Labels: labels,
		IPAM: &network.IPAM{
			Driver: "default",
			Config: []network.IPAMConfig{
				{Subnet: cidr, Gateway: gw},
			},
		},
		Options: map[string]string{
			"com.docker.network.bridge.enable_ip_masquerade": "true",
			"com.docker.network.bridge.enable_icc":           "true",
		},
	})
	if err != nil {
		return "", fmt.Errorf("create docker bridge network %s: %w", name, err)
	}
	return resp.ID, nil
}

// RemoveBridgeNetwork removes a Docker-managed bridge network by ID.
func (s *Substrate) RemoveBridgeNetwork(ctx context.Context, networkID string) error {
	return s.cli.NetworkRemove(ctx, networkID)
}

// CreateManagedNetwork implements substrate.Substrate by creating a Docker
// bridge network. Currently always NAT (NAT field is informational).
func (s *Substrate) CreateManagedNetwork(ctx context.Context, spec substrate.ManagedNetworkSpec) (substrate.ManagedNetworkInfo, error) {
	netName := fmt.Sprintf("sysbox-nat-%s", spec.Name)
	id, err := s.CreateBridgeNetwork(ctx, netName, spec.CIDR, spec.Labels)
	if err != nil {
		return substrate.ManagedNetworkInfo{}, err
	}
	return substrate.ManagedNetworkInfo{ID: id, Name: netName}, nil
}

// RemoveManagedNetwork implements substrate.Substrate.
func (s *Substrate) RemoveManagedNetwork(ctx context.Context, id string) error {
	return s.RemoveBridgeNetwork(ctx, id)
}

// ReadManagedNetwork queries an existing Docker network by name without
// creating one. Tries the raw name first, then the sysbox-prefixed variant.
func (s *Substrate) ReadManagedNetwork(ctx context.Context, spec substrate.ManagedNetworkSpec) (substrate.ManagedNetworkInfo, error) {
	// Try the user-given name directly (e.g. "bridge", "mynet").
	if existing, err := s.cli.NetworkInspect(ctx, spec.Name, network.InspectOptions{}); err == nil {
		return substrate.ManagedNetworkInfo{ID: existing.ID, Name: existing.Name}, nil
	}
	// Fall back to sysbox-prefixed name.
	prefixed := fmt.Sprintf("sysbox-nat-%s", spec.Name)
	if existing, err := s.cli.NetworkInspect(ctx, prefixed, network.InspectOptions{}); err == nil {
		return substrate.ManagedNetworkInfo{ID: existing.ID, Name: existing.Name}, nil
	}
	return substrate.ManagedNetworkInfo{}, fmt.Errorf("docker network %q not found (also tried %q)", spec.Name, prefixed)
}

// ConnectContainerToNetwork attaches a running container to a Docker network
// with a static IP address. Used for nat=true networks.
func (s *Substrate) ConnectContainerToNetwork(ctx context.Context, containerID, networkID, ip string) error {
	// Strip prefix length for Docker API (it wants bare IP, not CIDR).
	host, _, err := net.ParseCIDR(ip)
	if err != nil {
		// ip may already be a bare address.
		host = net.ParseIP(ip)
		if host == nil {
			return fmt.Errorf("invalid IP address %q", ip)
		}
	}

	return s.cli.NetworkConnect(ctx, networkID, containerID, &network.EndpointSettings{
		IPAMConfig: &network.EndpointIPAMConfig{
			IPv4Address: host.String(),
		},
	})
}

// firstHost returns the first usable host IP in a subnet as a string.
// e.g. 172.20.0.0/24 → "172.20.0.1"
func firstHost(n *net.IPNet) string {
	ip := make(net.IP, len(n.IP))
	copy(ip, n.IP)
	ip[len(ip)-1]++
	return ip.String()
}
