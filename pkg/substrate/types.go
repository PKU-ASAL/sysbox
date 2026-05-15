// Package substrate defines the abstraction that every substrate driver
// (docker, firecracker, libvirt) must implement.
//
// Phase 1 only has docker. Phase 3 adds firecracker and libvirt.
package substrate

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

type Capabilities struct {
	SharedKernel    bool
	SupportsWindows bool
	BootTime        string // "ms" | "seconds"
	NICType         string // "veth" | "tap"
}

// ObservationTarget tells the sensor provider how to attach to this node.
// Phase 1 uses only "host-pid-namespace" (docker); virtio-serial comes in Phase 3.
type ObservationTarget struct {
	Kind  string // "host-pid-namespace" | "virtio-serial"
	Value string
}
