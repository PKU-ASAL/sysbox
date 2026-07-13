// Package substrate defines the abstraction that every substrate driver
// (docker, firecracker, libvirt) must implement.
//
// v1.0 supports docker + firecracker; libvirt is in flight (W2).
package substrate

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotSupported is returned by optional interface methods whose default
// implementation in BaseSubstrate is a no-op or unsupported operation.
var ErrNotSupported = errors.New("operation not supported by this substrate")

// ManagedNetworkSpec describes a substrate-managed network to create.
type ManagedNetworkSpec struct {
	Name   string            // resource name used to derive the network's system identifier
	CIDR   string            // IP address range, e.g. "172.20.0.0/24"
	NAT    bool              // true → internet access via masquerade
	Labels map[string]string // provider metadata for recovery/cleanup
}

// ManagedNetworkInfo is returned by CreateManagedNetwork.
type ManagedNetworkInfo struct {
	ID   string // substrate-specific network identifier (docker network ID, etc.)
	Name string // the system-level name (bridge name, network name)
}

// ImageSpec describes how to obtain a node image. Exactly one source field
// should be set; substrates only inspect the field(s) they understand.
//
//   - DockerRef — docker image tag/digest (docker substrate)
//   - Rootfs    — ext4 rootfs file or URL (firecracker substrate)
//   - QCow2     — qcow2 disk image file or URL (libvirt substrate)
type ImageSpec struct {
	DockerRef string
	Rootfs    string
	QCow2     string
	Size      string
}

type ImageRef struct {
	ID         string
	Repository string
}

// NodeSpec carries substrate-neutral coordinates for creating a node.
// Substrate-specific options (privileged, kernel, vcpus, ...) live in
// ProviderConfig, a substrate-owned typed value produced by
// Substrate.DecodeProviderConfig.
type NodeSpec struct {
	Name    string
	Image   ImageRef
	VCPUs   int
	Memory  string
	Env     map[string]string
	Sysctls map[string]string
	Labels  map[string]string
	Ports   []PortSpec
	// ManagedNetwork signals that an external network manager must attach at
	// least one declared network after node creation.
	ManagedNetwork bool

	// ProviderConfig is a substrate-owned typed value (e.g. *docker.Config,
	// *firecracker.Config) produced by Substrate.DecodeProviderConfig. It is
	// opaque to runtime; substrates type-assert in their own CreateNode.
	ProviderConfig any
}

type PortSpec struct {
	Name      string `json:"name,omitempty"`
	Target    int    `json:"target"`
	Published int    `json:"published,omitempty"`
	Protocol  string `json:"protocol,omitempty"`
	Exposure  string `json:"exposure,omitempty"`
	HostIP    string `json:"host_ip,omitempty"`
}

type ResolvedPort struct {
	Name       string `json:"name,omitempty"`
	Target     int    `json:"target"`
	Published  int    `json:"published,omitempty"`
	Protocol   string `json:"protocol"`
	Exposure   string `json:"exposure"`
	Host       string `json:"host,omitempty"`
	URL        string `json:"url,omitempty"`
	TargetHost string `json:"target_host,omitempty"`
}

const (
	PortExposureNone   = "none"
	PortExposureDirect = "direct"
	PortExposureHost   = "host"
)

// ProviderDeps lists resource references a substrate's typed Config holds.
// Runtime translates these into graph edges so the resources get applied
// before the node is created. Substrates that have no provider block (or no
// cross-resource refs) return an empty value.
type ProviderDeps struct {
	Kernels  []string // sysbox_kernel resource names
	Images   []string // sysbox_image resource names
	Networks []string // sysbox_network resource names
}

// NodeHandle is the substrate-neutral reference to a created node.
//
// Layout:
//
//	ID       – substrate-defined identifier (container_id, vm_id, libvirt domain UUID, ...).
//	           Stable across the node's lifetime; persisted in state.
//	Net      – substrate-neutral network coordinates (primary IP). Populated post-Start.
//	Conn     – preferred control-plane channel (docker-exec / ssh / vsock / ...).
//	Provider – substrate-owned typed value; opaque to runtime. Substrates may put
//	           any data here (vm_dir, socket path, vsock CID, etc.) and recover it
//	           on subsequent calls. Persisted in state via Marshal/UnmarshalProviderState.
type NodeHandle struct {
	ID       string
	Net      NetInfo
	Conn     ConnInfo
	Provider any
}

type NodeLifecycleStatus string

const (
	NodeStatusUnknown   NodeLifecycleStatus = "unknown"
	NodeStatusMissing   NodeLifecycleStatus = "missing"
	NodeStatusRunning   NodeLifecycleStatus = "running"
	NodeStatusExited    NodeLifecycleStatus = "exited"
	NodeStatusPaused    NodeLifecycleStatus = "paused"
	NodeStatusUnhealthy NodeLifecycleStatus = "unhealthy"
)

// NodeObservation is the substrate-neutral lifecycle snapshot for a node.
// Docker derives it from dockerd; Firecracker derives it from pid/socket/vsock
// anchors; libvirt can derive it from domain state.
type NodeObservation struct {
	Exists     bool                `json:"exists"`
	Running    bool                `json:"running"`
	Healthy    bool                `json:"healthy"`
	Adopted    bool                `json:"adopted,omitempty"`
	Status     NodeLifecycleStatus `json:"status"`
	PID        int                 `json:"pid,omitempty"`
	ExitCode   *int                `json:"exit_code,omitempty"`
	ExternalID string              `json:"external_id,omitempty"`
	Reason     string              `json:"reason,omitempty"`
	LastSeen   time.Time           `json:"last_seen,omitempty"`
}

// NetInfo carries substrate-neutral network info for a node.
type NetInfo struct {
	// PrimaryIP is the node's primary IPv4 address (CIDR stripped), used by
	// Connection factories. Empty if not applicable (yet).
	PrimaryIP string
}

// ConnectionKind enumerates control-plane channel types.
type ConnectionKind string

const (
	ConnKindNone   ConnectionKind = ""
	ConnKindDocker ConnectionKind = "docker"
	ConnKindSSH    ConnectionKind = "ssh"
	ConnKindVsock  ConnectionKind = "vsock"
	ConnKindWinRM  ConnectionKind = "winrm"
)

// ConnInfo carries the preferred control-plane channel coordinates for a node.
type ConnInfo struct {
	Kind     ConnectionKind
	Endpoint string // substrate-defined: container ID, "host:port", "uds-path:port", ...
	User     string // optional
}

// ConnectionHint carries optional HCL-level overrides for connection selection.
// The substrate may ignore these if its auto-selection (from NodeHandle.Conn)
// already picks the right channel.
type ConnectionHint struct {
	Type       string // explicit type from HCL: "docker" | "ssh" | "vsock" | "auto"
	Host       string // SSH host override
	User       string // SSH user
	Password   string // SSH password
	PrivateKey string // SSH private key path
}

// Connection is the substrate-agnostic interface for reaching a running node
// (exec, copy, background). Each substrate returns its own implementation.
// Moved here from pkg/transport so substrates can implement it without
// import cycles.
type Connection interface {
	// ExecInline runs each line as a shell command (sh -c) sequentially.
	// stdout and stderr are written to os.Stdout / os.Stderr.
	// Returns on first non-zero exit.
	ExecInline(ctx context.Context, cmds []string) error

	// ExecStream runs cmds sequentially, writing stdout and stderr to the
	// provided writers. Useful for streaming output over HTTP or to a log.
	ExecStream(ctx context.Context, cmds []string, stdout, stderr io.Writer) error

	// ExecBackground starts a command detached from the calling session.
	// Returns the PID of the spawned process.
	ExecBackground(ctx context.Context, cmd []string, env map[string]string) (int, error)

	// CopyFile copies a local file into the node at dstPath.
	CopyFile(ctx context.Context, srcPath, dstPath string) error
}

// ConnectionWaiter is an optional Connection capability: block until the
// transport is ready to execute commands (SSH reachable, vsock agent up).
// The runtime probes for it before running provisioners so it never has to
// know concrete transport types.
type ConnectionWaiter interface {
	WaitReady(ctx context.Context, timeout time.Duration) error
}

// ImageEntryStarter is an optional Substrate capability: launch the image's
// original entrypoint/CMD inside an already-running node. Substrates that
// override the entrypoint at create time (e.g. docker's "sleep infinity" so
// provisioners can run first) implement this; the runtime probes for it after
// provisioning. A no-op return means the image had no entry to start.
type ImageEntryStarter interface {
	ExecImageEntry(ctx context.Context, handle NodeHandle) error
}

// ConsoleProvider is an optional substrate capability for an interactive
// console session. It is intentionally separate from Connection: provisioners
// need simple command execution, while browser consoles need bidirectional
// stdin/stdout, TTY sizing, and lifecycle control.
type ConsoleProvider interface {
	OpenConsole(ctx context.Context, handle NodeHandle, req ConsoleRequest) (ConsoleSession, error)
}

type ConsoleRequest struct {
	Cmd     []string          `json:"cmd,omitempty"`
	Shell   string            `json:"shell,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	WorkDir string            `json:"work_dir,omitempty"`
	TTY     bool              `json:"tty"`
	Cols    int               `json:"cols,omitempty"`
	Rows    int               `json:"rows,omitempty"`
}

type ConsoleSession interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Stderr() io.Reader
	Resize(ctx context.Context, cols, rows int) error
	Wait() (int, error)
	Close() error
}

// HandlePublicAttributes returns provider-independent computed attributes.
func HandlePublicAttributes(h NodeHandle) map[string]any {
	return map[string]any{
		"container_id": h.ID,
		"primary_ip":   h.Net.PrimaryIP,
	}
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
	NICKindVeth      = "veth"
	NICKindTap       = "tap"
	NICKindMacvtap   = "macvtap"
	NICKindVFIO      = "vfio"
	NICKindDockerNAT = "docker-nat" // Docker-managed bridge network (docker network connect)
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
	NICHotPlug      bool          // attachment lifecycle works after StartNode (true=container; false=microVM/VM cold-plug)
	DiskHotPlug     bool          // attach extra disks after StartNode
	NICKinds        []string      // device types this substrate can produce, e.g. ["veth"] or ["tap","macvtap"]
	ConsoleKinds    []string      // attachable console modes
	NeedsCloudinit  bool          // PrepareImage / CreateNode requires a cloudinit seed
	PIDVisibility   PIDMode       // how guest PIDs relate to host PID space
	SupportsPause   bool          // Substrate.Pause/Resume implemented (W3)
	PortExposures   []string      // supported port exposure modes: none, direct, host
	BootTime        time.Duration // typical boot latency (best-effort estimate)
	Notes           string        // free-form documentation, displayed in `sysbox plan`
}
