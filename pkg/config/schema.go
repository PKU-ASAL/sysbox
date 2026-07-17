// Package config defines sysbox's HCL schema and parser.
//
// Resource types: sysbox_image, sysbox_node, sysbox_network, sysbox_kernel,
// sysbox_router, sysbox_firewall, sysbox_ssh_access.
package config

import "github.com/hashicorp/hcl/v2"

// Root is the top-level parsed HCL document.
type Root struct {
	Substrates []SubstrateBlock `hcl:"substrate,block"`
	Variables  []VariableBlock  `hcl:"variable,block"`
	Modules    []ModuleBlock    `hcl:"module,block"`
	Resources  []ResourceBlock  `hcl:"resource,block"`
	Data       []DataBlock      `hcl:"data,block"`
	Locals     []LocalsBlock    `hcl:"locals,block"`
	Outputs    []OutputBlock    `hcl:"output,block"`
}

// VariableBlock declares an input variable for a module file.
//
//	variable "cidr_dmz" {
//	  default = "10.0.1.0/24"
//	}
type VariableBlock struct {
	Name   string   `hcl:"name,label"`
	Remain hcl.Body `hcl:",remain"` // may contain default = <expr>
}

// ModuleBlock instantiates a reusable HCL topology fragment.
//
//	module "lab_net" {
//	  source        = "./modules/three-tier-net.sysbox.hcl"
//	  cidr_dmz      = "10.0.1.0/24"
//	  cidr_internal = "10.0.2.0/24"
//	}
//
// All attributes except source are passed as var.<name> to the module file.
// Module resources keep their declared name and carry a structured module path.
// Module outputs are accessible as module.<name>.<output_key> in the caller.
type ModuleBlock struct {
	Name   string   `hcl:"name,label"`
	Source string   `hcl:"source"`
	Remain hcl.Body `hcl:",remain"` // variable assignments
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

// DataBlock is a read-only query block:
//
//	data "sysbox_node" "existing" {
//	  substrate = substrate.docker.dk
//	  id        = "my-container"
//	}
//
// Unlike resources, data blocks do not create infrastructure; they query
// existing nodes and make attributes available in the eval context so
// other resources can reference them (e.g. data.sysbox_node.existing.ip).
type DataBlock struct {
	Type   string   `hcl:"type,label"`
	Name   string   `hcl:"name,label"`
	Remain hcl.Body `hcl:",remain"`
}

// DataNodeConfig is the decoded form of data "sysbox_node" blocks.
type DataNodeConfig struct {
	Substrate string `hcl:"substrate"`
	ID        string `hcl:"id"` // container name, domain name, etc.
}

// DataNetworkConfig is the decoded form of data "sysbox_network" blocks.
type DataNetworkConfig struct {
	Name string `hcl:"name"` // docker network name or bridge name
}

// DataImageConfig is the decoded form of data "sysbox_image" blocks.
// Allows querying an existing image's metadata (e.g. docker image inspect).
type DataImageConfig struct {
	Substrate    string `hcl:"substrate"`
	Kind         string `hcl:"kind"`
	Source       string `hcl:"source"`
	Architecture string `hcl:"architecture"`
	GuestFamily  string `hcl:"guest_family"`
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
	Name        string         `hcl:"name,label"`
	Value       hcl.Expression `hcl:"value"`
	Description string         `hcl:"description,optional"`
	Remain      hcl.Body       `hcl:",remain"`
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
//	  program = "/usr/bin/install"
//	  args    = ["-d", "/opt/lab"]
//	  shell   = "none"
//	}
//
//	provisioner "file" {
//	  source      = "configs/foo.json"
//	  destination = "/etc/foo.json"
//	}
type ProvisionerConfig struct {
	Type        string            `hcl:"type,label"`
	Program     string            `hcl:"program,optional"`
	Args        []string          `hcl:"args,optional"`
	Environment map[string]string `hcl:"environment,optional"`
	WorkingDir  string            `hcl:"working_dir,optional"`
	Shell       string            `hcl:"shell,optional"`
	Source      string            `hcl:"source,optional"`
	Destination string            `hcl:"destination,optional"`
	Background  bool              `hcl:"background,optional"`
}

// Typed representations after second-pass decoding.

// NodeConfig is the substrate-neutral HCL shape for `resource "sysbox_node"`.
// LifecycleConfig is the optional `lifecycle { ... }` sub-block shared by
// sysbox_node and sysbox_network resources.
//
//	resource "sysbox_node" "db" {
//	  ...
//	  lifecycle {
//	    prevent_destroy = true
//	    ignore_changes  = ["image"]
//	  }
//	}
type LifecycleConfig struct {
	// PreventDestroy prevents `sysbox destroy` from removing this resource.
	// The destroy command will print a warning and skip the resource.
	PreventDestroy bool `hcl:"prevent_destroy,optional"`
	// IgnoreChanges lists attribute names that should be excluded from drift
	// detection. When a resource is flagged as Changed (drift), attributes
	// listed here are not considered for re-creation.
	IgnoreChanges []string `hcl:"ignore_changes,optional"`
}

// Substrate-specific options (privileged, kernel, vcpus, ...) live in a
// nested `provider "X" {}` block decoded by the substrate itself via
// Substrate.DecodeProviderConfig.
type NodeConfig struct {
	Image        string              `hcl:"image"`
	Substrate    string              `hcl:"substrate"` // "docker" | "firecracker" | ...
	GuestFamily  string              `hcl:"guest_family,optional"`
	Vcpus        int                 `hcl:"vcpus,optional"`
	Memory       string              `hcl:"memory,optional"` // e.g. "512" (MB)
	Env          map[string]string   `hcl:"env,optional"`
	DependsOn    []string            `hcl:"depends_on,optional"`
	Links        []LinkConfig        `hcl:"link,block"`
	Ports        []PortConfig        `hcl:"port,block"`
	Routes       []RouteConfig       `hcl:"route,block"`
	Connections  []ConnectionConfig  `hcl:"connection,block"`
	Provisioners []ProvisionerConfig `hcl:"provisioner,block"`
	Providers    []ProviderBlock     `hcl:"provider,block"`
	Lifecycle    *LifecycleConfig    `hcl:"lifecycle,block"`
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
	Name    string `hcl:"name,label"`
	Network string `hcl:"network"`
	IP      string `hcl:"ip"`
	Gateway string `hcl:"gw,optional"`
	MAC     string `hcl:"mac,optional"`
}

// PortConfig declares a node-local port and how sysbox should expose it.
//
//	port {
//	  name      = "http"
//	  target    = 80
//	  protocol  = "tcp"
//	  exposure  = "host"
//	  published = 28080
//	}
//
// target is the guest/container port. published is the host port when
// exposure="host". exposure defaults to "direct"; protocol defaults to "tcp".
type PortConfig struct {
	Name      string `hcl:"name,optional"`
	Target    int    `hcl:"target"`
	Published int    `hcl:"published,optional"`
	Protocol  string `hcl:"protocol,optional"`
	Exposure  string `hcl:"exposure,optional"`
	HostIP    string `hcl:"host_ip,optional"`
}

// RouteConfig declares a static route inside a node (Terraform-style declarative
// replacement for `ip route add` in provisioners). sysbox configures the route
// after the node is created and NICs are attached, and tracks it in state for
// drift detection.
//
//	route { dst = "10.0.2.0/24"; via = "10.0.1.254" }
type RouteConfig struct {
	Destination string `hcl:"dst"` // CIDR, e.g. "10.0.2.0/24" or "0.0.0.0/0"
	Via         string `hcl:"via"` // gateway IP, e.g. "10.0.1.254"
}

type NetworkConfig struct {
	CIDR      string           `hcl:"cidr"`
	Type      string           `hcl:"type,optional"`
	NAT       bool             `hcl:"nat,optional"`
	Lifecycle *LifecycleConfig `hcl:"lifecycle,block"`
}

type ImageConfig struct {
	Substrate    string `hcl:"substrate"`
	Kind         string `hcl:"kind"`
	Source       string `hcl:"source"`
	SHA256       string `hcl:"sha256,optional"`
	Architecture string `hcl:"architecture"`
	GuestFamily  string `hcl:"guest_family"`
	Size         string `hcl:"size,optional"`
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
	Substrate    string `hcl:"substrate"`
	Architecture string `hcl:"architecture"`
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
	AttachTo       string         `hcl:"attach_to"`
	Family         string         `hcl:"family,optional"`
	DefaultInput   string         `hcl:"default_input,optional"`
	DefaultOutput  string         `hcl:"default_output,optional"`
	DefaultForward string         `hcl:"default_forward,optional"`
	Rules          []FirewallRule `hcl:"rule,block"`
}

type FirewallRule struct {
	Name             string   `hcl:"name,label"`
	Direction        string   `hcl:"direction"`
	SourceCIDRs      []string `hcl:"source_cidrs,optional"`
	DestinationCIDRs []string `hcl:"destination_cidrs,optional"`
	SourcePorts      []string `hcl:"source_ports,optional"`
	DestinationPorts []string `hcl:"destination_ports,optional"`
	Protocol         string   `hcl:"protocol,optional"`
	InputAttachment  string   `hcl:"input_attachment,optional"`
	OutputAttachment string   `hcl:"output_attachment,optional"`
	States           []string `hcl:"states,optional"`
	Verdict          string   `hcl:"verdict"`
	Counter          bool     `hcl:"counter,optional"`
	Log              bool     `hcl:"log,optional"`
}

type RouterConfig struct {
	Substrate  string            `hcl:"substrate"`
	Image      string            `hcl:"image"`
	Interfaces []RouterInterface `hcl:"interface,block"`
	NatFrom    string            `hcl:"nat_from,optional"` // interface name (label)
	NatTo      string            `hcl:"nat_to,optional"`   // interface name (label)
	Lifecycle  *LifecycleConfig  `hcl:"lifecycle,block"`
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
