// Package config defines sysbox's HCL schema and parser.
//
// Resource types: sysbox_image, sysbox_node, sysbox_network, sysbox_kernel,
// sysbox_router, sysbox_firewall, sysbox_ssh_access, sysbox_actor.
package config

import "github.com/hashicorp/hcl/v2"

// Root is the top-level parsed HCL document.
type Root struct {
	Substrates []SubstrateBlock `hcl:"substrate,block"`
	Resources  []ResourceBlock  `hcl:"resource,block"`
	Locals     []LocalsBlock    `hcl:"locals,block"`
	Outputs    []OutputBlock    `hcl:"output,block"`
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

// ForEachHeader is decoded first from a ResourceBlock.Remain to extract the
// optional for_each meta-argument before type-specific decoding.
//
//	resource "sysbox_node" "victims" {
//	  for_each = { web = "10.0.2.10", db = "10.0.2.20" }
//	  ...
//	}
//
// Remain contains every attribute except for_each and is passed to
// DecodeResource for the type-specific schema decode.
type ForEachHeader struct {
	ForEach hcl.Expression `hcl:"for_each,optional"`
	Remain  hcl.Body       `hcl:",remain"`
}

// LocalsBlock holds a set of local value definitions.
//
//	locals {
//	  cidr_prefix = "10.0"
//	  dmz_cidr    = "10.0.1.0/24"
//	}
type LocalsBlock struct {
	Remain hcl.Body `hcl:",remain"`
}

// OutputBlock exposes a value after apply.
//
//	output "attacker_ip" {
//	  value       = sysbox_node.node_attack.id
//	  description = "IP of the attacker node"
//	}
type OutputBlock struct {
	Name        string   `hcl:"name,label"`
	Value       string   `hcl:"value"`
	Description string   `hcl:"description,optional"`
	Remain      hcl.Body `hcl:",remain"`
}

// ConnectionConfig describes how provisioners reach a node.
//
//	connection {
//	  type        = "auto"  # docker | ssh | vsock
//	  host        = "..."   # for ssh
//	  user        = "root"
//	  private_key = "..."
//	}
type ConnectionConfig struct {
	Type       string `hcl:"type,optional"`
	Host       string `hcl:"host,optional"`
	User       string `hcl:"user,optional"`
	Password   string `hcl:"password,optional"`
	PrivateKey string `hcl:"private_key,optional"`
}

// ProvisionerConfig represents a single provisioner block inside a node.
//
//	provisioner "exec" {
//	  inline     = ["mkdir -p /root/.ssh"]
//	  background = false
//	}
//
//	provisioner "file" {
//	  source      = "configs/foo.json"
//	  destination = "/etc/foo.json"
//	}
type ProvisionerConfig struct {
	Type        string   `hcl:"type,label"`
	Inline      []string `hcl:"inline,optional"`
	Source      string   `hcl:"source,optional"`
	Destination string   `hcl:"destination,optional"`
	Background  bool     `hcl:"background,optional"`
}

// Typed representations after second-pass decoding.

// NodeConfig is the substrate-neutral HCL shape for `resource "sysbox_node"`.
// Substrate-specific options (privileged, kernel, vcpus, ...) live in a
// nested `provider "X" {}` block decoded by the substrate itself via
// Substrate.DecodeProviderConfig.
type NodeConfig struct {
	Image        string              `hcl:"image"`
	Substrate    string              `hcl:"substrate"` // "docker" | "firecracker" | ...
	Vcpus        int                 `hcl:"vcpus,optional"`
	Memory       string              `hcl:"memory,optional"` // e.g. "512" (MB)
	Env          map[string]string   `hcl:"env,optional"`
	DependsOn    []string            `hcl:"depends_on,optional"`
	Links        []LinkConfig        `hcl:"link,block"`
	Connections  []ConnectionConfig  `hcl:"connection,block"`
	Provisioners []ProvisionerConfig `hcl:"provisioner,block"`
	Providers    []ProviderBlock     `hcl:"provider,block"`
	Sensor       bool                `hcl:"sensor,optional"`

	// ProviderConfig is filled by the loader after the substrate is resolved
	// (substrate.DecodeProviderConfig). Not part of the HCL surface; gohcl
	// ignores fields with no `hcl:` tag.
	ProviderConfig any
}

// ProviderBlock is the labelled `provider "X" {}` block under a sysbox_node;
// the label must match the chosen substrate type. Decoded lazily by the
// loader via substrate.DecodeProviderConfig so substrates own their schema.
type ProviderBlock struct {
	Type   string   `hcl:"type,label"`
	Remain hcl.Body `hcl:",remain"`
}

type LinkConfig struct {
	Network string `hcl:"network"`
	IP      string `hcl:"ip"`
	Gateway string `hcl:"gw,optional"`
}

type NetworkConfig struct {
	CIDR string `hcl:"cidr"`
	Type string `hcl:"type,optional"`
	NAT  bool   `hcl:"nat,optional"`
}

// ActorConfig declares an ACP-driven actor (attacker, noise user, etc.).
//
// position = "internal"  — exec the command inside an existing sysbox_node.
//
//	The actor shares the node's network and filesystem.
//	Equivalent to an internal actor.
//
// position = "external"  — create a standalone container outside the topology.
//
//	                         The actor only reaches the topology through declared
//	                         network links (entry_points is informational metadata).
//
//		resource "sysbox_actor" "red" {
//		  position = "internal"
//		  node     = sysbox_node.node_attack.id
//		  command  = ["opencode", "serve", "--port", "4096", "--hostname", "0.0.0.0"]
//		  port     = 4096
//		  env      = { DEEPSEEK_API_KEY = env("DEEPSEEK_API_KEY") }
//		}
//
//		resource "sysbox_actor" "scanner" {
//		  position = "external"
//		  image    = sysbox_image.attacker.id
//		  link {
//		    network = sysbox_network.net_uplink.id
//		    ip      = "172.20.0.30/24"
//		    gw      = "172.20.0.1"
//		  }
//		  command = ["opencode", "serve", "--port", "4097", "--hostname", "0.0.0.0"]
//		  port    = 4097
//		  entry_points = { web = "http://172.20.0.10", ssh = "ssh://172.20.0.10:22" }
//		}
type ActorConfig struct {
	Position    string            `hcl:"position,optional"` // "internal" (default) | "external"
	Node        string            `hcl:"node,optional"`     // internal: target sysbox_node ref
	Image       string            `hcl:"image,optional"`    // external: sysbox_image ref
	Links       []LinkConfig      `hcl:"link,block"`        // external: network attachments
	Command     []string          `hcl:"command"`
	Port        int               `hcl:"port,optional"`
	Env         map[string]string `hcl:"env,optional"`
	EntryPoints map[string]string `hcl:"entry_points,optional"` // informational: accessible endpoints
	DependsOn   []string          `hcl:"depends_on,optional"`
}

type ImageConfig struct {
	Substrate string `hcl:"substrate"`
	DockerRef string `hcl:"docker_ref,optional"`
	// Rootfs is either a local path (e.g. "/tmp/rootfs.ext4") or a URL
	// ("https://.../rootfs.ext4"). URLs are fetched via pkg/artifact at
	// apply time and cached on disk.
	Rootfs string `hcl:"rootfs,optional"`
	// SHA256, if set, is verified against the resolved artifact (URL or
	// local). Mismatch fails apply.
	SHA256 string `hcl:"sha256,optional"`
	Size   string `hcl:"size,optional"`
}

// KernelConfig is the schema for `resource "sysbox_kernel" "<name>" { ... }`.
//
// A kernel resource represents a single fetchable vmlinux binary (or other
// kernel image). It is referenced from sysbox_node via:
//
//	resource "sysbox_node" "vm" {
//	  kernel = sysbox_kernel.fc_510.id
//	  ...
//	}
//
// At apply time, the artifact resolver downloads the source if needed,
// verifies sha256, and stores the local cache path in state. The destroy
// op removes the state entry but never deletes the cache file (it is a
// shared, content-addressed cache).
type KernelConfig struct {
	Substrate string `hcl:"substrate"`
	// Source is the artifact reference. Supported schemes:
	//   - "https://..." / "http://..."   (downloaded into the cache)
	//   - "/abs/path"                    (local file, no copy)
	//   - "relative/path"                (resolved against cwd)
	Source string `hcl:"source"`
	// SHA256 is the expected hex digest of the kernel image (optional).
	// When set, the resolver verifies and the cache key becomes
	// content-addressed.
	SHA256 string `hcl:"sha256,optional"`
	// CmdlineTemplate, if set, overrides the substrate's default kernel
	// command line for nodes that reference this kernel. Reserved for
	// future use; not yet consumed by the firecracker substrate.
	CmdlineTemplate string   `hcl:"cmdline_template,optional"`
	DependsOn       []string `hcl:"depends_on,optional"`
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
