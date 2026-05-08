// Package config defines sysbox's HCL schema and parser.
//
// Phase 1 supports:
//   - substrate block (only type="docker")
//   - sysbox_image, sysbox_node, sysbox_network, sysbox_link,
//     sysbox_firewall, sysbox_router, sysbox_ssh_access
//
// Firecracker/libvirt substrates and sensor resources are Phase 2/3.
package config

import "github.com/hashicorp/hcl/v2"

// Root is the top-level parsed HCL document.
type Root struct {
	Substrates []SubstrateBlock `hcl:"substrate,block"`
	Resources  []ResourceBlock  `hcl:"resource,block"`
}

// SubstrateBlock corresponds to:
//
//	substrate "docker" { alias = "light" }
type SubstrateBlock struct {
	Type   string   `hcl:"type,label"`
	Alias  string   `hcl:"alias"`
	Remain hcl.Body `hcl:",remain"`
}

// ResourceBlock is every "resource" in the HCL file:
//
//	resource "sysbox_node" "web" { image = ...; links = [...] }
type ResourceBlock struct {
	Type   string   `hcl:"type,label"`
	Name   string   `hcl:"name,label"`
	Remain hcl.Body `hcl:",remain"`
}

// Typed representations after second-pass decoding.

type NodeConfig struct {
	Image     string            `hcl:"image"`
	Substrate string            `hcl:"substrate"`
	Vcpus     int               `hcl:"vcpus,optional"`
	Memory    string            `hcl:"memory,optional"`
	Env       map[string]string `hcl:"env,optional"`
	Links     []LinkConfig      `hcl:"link,block"`
}

type LinkConfig struct {
	Network string `hcl:"network"`
	IP      string `hcl:"ip"`
	Gateway string `hcl:"gw,optional"`
}

type NetworkConfig struct {
	CIDR string `hcl:"cidr"`
	Type string `hcl:"type,optional"`
}

type ImageConfig struct {
	Substrate string `hcl:"substrate"`
	DockerRef string `hcl:"docker_ref,optional"`
	Rootfs    string `hcl:"rootfs,optional"`
	Size      string `hcl:"size,optional"`
}

type FirewallConfig struct {
	AttachTo string         `hcl:"attach_to"`
	Rules    []FirewallRule `hcl:"rule,block"`
}

type FirewallRule struct {
	Proto  string `hcl:"proto"`
	DPort  int    `hcl:"dport,optional"`
	SrcNet string `hcl:"src_net,optional"`
	Action string `hcl:"action"`
}

type RouterConfig struct {
	Substrate  string            `hcl:"substrate"`
	Image      string            `hcl:"image"`
	Interfaces []RouterInterface `hcl:"interface,block"`
	NatFrom    string            `hcl:"nat_from,optional"` // interface name (label)
	NatTo      string            `hcl:"nat_to,optional"`   // interface name (label)
}

type RouterInterface struct {
	Name    string `hcl:"name,label"`
	Network string `hcl:"network"`
	IP      string `hcl:"ip"`
}

type SSHAccessConfig struct {
	Node           string   `hcl:"node"`
	AuthorizedKeys []string `hcl:"authorized_keys"`
	BindIP         string   `hcl:"bind_ip,optional"`
	Port           int      `hcl:"port,optional"`
}
