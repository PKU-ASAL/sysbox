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

type NodeSpec struct {
	Name    string
	Image   ImageRef
	VCPUs   int
	Memory  string
	Env     map[string]string
	Sysctls map[string]string // passed to container runtime at create time
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
