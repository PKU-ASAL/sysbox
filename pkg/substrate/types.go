// Package substrate defines the abstraction that every substrate driver
// (docker, firecracker, libvirt) must implement.
//
// v1.0 supports docker + firecracker; libvirt is in flight (W2).
package substrate

import "time"

type ImageSpec struct {
	DockerRef string
	Rootfs    string
	Size      string
}

type ImageRef struct {
	ID         string
	Repository string
}

// DockerNetworkAttachment describes a Docker-managed bridge network to connect
// at container-creation time (not via post-start docker network connect).
// Attaching at creation keeps NetworkMode:"none" for veth-only nodes while
// allowing Docker NAT bridges (internet uplink) on nodes that need both.
// For microVM/VM substrates this maps to a TAP device on the equivalent bridge.
type DockerNetworkAttachment struct {
	NetworkID string // Docker network ID
	IPv4      string // CIDR notation, e.g. "172.20.0.10/24"
}

type NodeSpec struct {
	Name              string
	Image             ImageRef
	VCPUs             int
	Memory            string
	Kernel            string // path to vmlinux (firecracker only)
	Rootfs            string // path to ext4 rootfs override (firecracker only)
	SSHUser           string
	SSHPass           string
	SSHPort           int
	Env               map[string]string
	Sysctls           map[string]string         // passed to container runtime at create time
	Privileged        bool                      // required for eBPF/tracee
	PidMode           string                    // "host" shares the host PID namespace
	CgroupnsMode      string                    // "host" shares the host cgroup namespace
	Binds             []string                  // host:container[:options] volume bind mounts
	InitialDockerNets []DockerNetworkAttachment // Docker bridge networks attached at create time

	// ChainInit (firecracker only) is the binary sysbox-init exec()s after
	// applying configuration. Defaults to /sbin/init, falls back to /bin/sh
	// inside the guest if missing. Empty string keeps the default behaviour.
	ChainInit string
}

type NodeHandle struct {
	ID         string
	Attributes map[string]any
}

type ExecSpec struct {
	Cmd     []string
	Env     map[string]string
	WorkDir string
}

type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type NIC struct {
	Kind       string // "veth" | "tap"
	HostEnd    string
	GuestEnd   string
	TargetName string // interface name inside the guest (e.g. "eth0", "eth1"); defaults to "eth0"
	MAC        string
	IP         string // CIDR notation e.g. "10.0.1.10/24"
	Gateway    string
	MTU        int
}

// PIDMode declares how guest processes are visible to the host.
//
//	PIDVisibilityHost   – guest PIDs share the host PID namespace (docker --pid=host)
//	PIDVisibilityNS     – guest has its own PID ns but is reachable from host (default docker)
//	PIDVisibilityOpaque – guest is an isolated kernel; host cannot see PIDs (microVM, VM)
type PIDMode string

const (
	PIDVisibilityHost   PIDMode = "host"
	PIDVisibilityNS     PIDMode = "ns"
	PIDVisibilityOpaque PIDMode = "opaque"
)

// NICKind enumerates link device types a substrate may produce.
const (
	NICKindVeth    = "veth"
	NICKindTap     = "tap"
	NICKindMacvtap = "macvtap"
	NICKindVFIO    = "vfio"
)

// ConsoleKind enumerates console attachment modes.
const (
	ConsoleKindTTY    = "tty"
	ConsoleKindSerial = "serial"
	ConsoleKindSPICE  = "spice"
	ConsoleKindVNC    = "vnc"
)

// Capabilities describes the substrate's runtime semantics. Runtime code uses
// these flags to make scheduling decisions (NIC hot-plug ordering, console
// selection, validation) without branching on substrate name.
//
// All bool defaults are the safe/conservative value (false means
// "unsupported"); BaseSubstrate provides usable defaults.
type Capabilities struct {
	SharedKernel    bool          // guest shares the host kernel (container)
	SupportsWindows bool          // can boot a Windows guest
	NICHotPlug      bool          // AttachNIC works after StartNode (true=container; false=microVM/VM cold-plug)
	DiskHotPlug     bool          // attach extra disks after StartNode
	NICKinds        []string      // device types this substrate can produce, e.g. ["veth"] or ["tap","macvtap"]
	ConsoleKinds    []string      // attachable console modes
	NeedsCloudinit  bool          // PrepareImage / CreateNode requires a cloudinit seed
	PIDVisibility   PIDMode       // how guest PIDs relate to host PID space
	SupportsPause   bool          // Substrate.Pause/Resume implemented (W3)
	BootTime        time.Duration // typical boot latency (best-effort estimate)
	Notes           string        // free-form documentation, displayed in `sysbox plan`
}

// ObservationTarget tells the sensor / monitor provider how to attach to this
// node. The substrate fills this in via ObservationHook so monitor backends
// don't need to know substrate-specific details.
type ObservationTarget struct {
	// Kind is one of:
	//   "host-pid-namespace"  – the value is the host PID of the guest's init
	//   "virtio-vsock"        – the value is "<uds-path>:<port>"
	//   "virtio-serial"       – the value is a host-side serial chardev path
	//   "ssh"                 – the value is "user@host:port"
	//   "winrm"               – the value is "host:port"
	//   "none"                – substrate does not expose any observation channel
	Kind  string
	Value string
}
