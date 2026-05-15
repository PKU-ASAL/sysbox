package docker

import (
	"context"
	"fmt"
	"net"

	"github.com/docker/docker/api/types/network"
)

// CreateBridgeNetwork creates a Docker-managed bridge network with NAT.
// Docker's bridge driver automatically sets up iptables MASQUERADE for outbound
// traffic, giving attached containers internet access via the host's default route.
// Returns the Docker network ID.
//
// Idempotent: if a network with the same name already exists (leftover from
// a failed apply), its ID is returned instead of failing with a name conflict.
func (s *Substrate) CreateBridgeNetwork(ctx context.Context, name, cidr string) (string, error) {
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

// ConnectContainerToNetwork attaches a running container to a Docker network
// with a static IP address. Used for nat=true networks.
func (s *Substrate) ConnectContainerToNetwork(ctx context.Context, containerID, networkID, ip string) error {
	// Strip prefix length for Docker API (it wants bare IP, not CIDR).
	host, _, err := net.ParseCIDR(ip)
	if err != nil {
		// ip may already be a bare address.
		host = net.ParseIP(ip)
		if host == nil {
			return fmt.Errorf("parse ip %s: %w", ip, err)
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
