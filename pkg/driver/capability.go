package driver

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/substrate"
)

type Capability string

const (
	CapabilityNode              Capability = "node"
	CapabilityNIC               Capability = "nic"
	CapabilitySnapshot          Capability = "snapshot"
	CapabilityGuestExec         Capability = "guest-exec"
	CapabilityGuestFiles        Capability = "guest-files"
	CapabilityNetwork           Capability = "network"
	CapabilityArtifact          Capability = "artifact"
	CapabilityImport            Capability = "import"
	CapabilityOwnershipAdoption Capability = "ownership_adoption"
	CapabilityNodeState         Capability = "node-state"
	CapabilityImageEntry        Capability = "image-entry"
	CapabilityPower             Capability = "power"
	CapabilityLinuxNetwork      Capability = "linux-network"
	CapabilityGuestNetwork      Capability = "guest-network"
	CapabilityGuestNetworkInit  Capability = "guest-network-init"
	CapabilityReset             Capability = "reset"
	CapabilityPolicy            Capability = "policy"
)

type Node interface {
	Capabilities() substrate.Capabilities
	PreflightChecks(bool) []substrate.PreflightCheck
	Validate(substrate.NodeSpec) error
	DecodeProviderConfig(hcl.Body, *hcl.EvalContext) (any, error)
	Dependencies(any) substrate.ProviderDeps
	PrepareHandle(context.Context, *substrate.NodeHandle, any, substrate.StateReader) error
	Connection(substrate.NodeHandle, []substrate.ConnectionHint) (substrate.Connection, error)
	CreateNode(context.Context, substrate.NodeSpec) (substrate.NodeHandle, error)
	StartNode(context.Context, substrate.NodeHandle) error
	StopNode(context.Context, substrate.NodeHandle) error
	DestroyNode(context.Context, substrate.NodeHandle) error
	NodeStatus(context.Context, substrate.NodeHandle) (bool, error)
	ObserveNode(context.Context, substrate.NodeHandle) (substrate.NodeObservation, error)
	AdoptNode(context.Context, substrate.NodeHandle) (substrate.NodeHandle, error)
}

// ManagedDiscovery locates a resource belonging to an already committed action.
// ErrorNotFound means proven absence, not failure to query the provider.
type ManagedDiscovery interface {
	DiscoverManagedNode(context.Context, substrate.OperationIdentity, string) (substrate.NodeHandle, error)
}

type NIC interface {
	Attach(context.Context, substrate.NodeHandle, AttachmentRequest) (AttachmentResult, error)
	Observe(context.Context, substrate.NodeHandle, AttachmentRequest, json.RawMessage) (AttachmentResult, error)
	Delete(context.Context, substrate.NodeHandle, AttachmentRequest, json.RawMessage) error
}

type AttachmentRequest struct {
	// NetworkIdentity belongs to the target network, never the attached node.
	NetworkIdentity substrate.OperationIdentity
	Name            string
	Network         address.Address
	MAC             string
	IPPrefixes      []string
	Gateway         string
	Aliases         []string
	NetworkState    json.RawMessage
}

type AttachmentResult struct {
	Driver      string
	GuestDevice string
	State       json.RawMessage
}

type Snapshot interface {
	CreateSnapshot(context.Context, substrate.NodeHandle, string) (string, error)
	RestoreSnapshot(context.Context, substrate.NodeHandle, string) error
	DeleteSnapshot(context.Context, string) error
}

type GuestExec interface {
	ExecInNode(context.Context, substrate.NodeHandle, substrate.ExecRequest) (substrate.ExecResult, error)
}

type GuestFiles interface {
	CopyToNode(context.Context, substrate.NodeHandle, string, string, uint32) error
}

type Network interface {
	CreateManagedNetwork(context.Context, substrate.ManagedNetworkSpec) (substrate.ManagedNetworkInfo, error)
	RemoveManagedNetwork(context.Context, string) error
	ReadManagedNetwork(context.Context, substrate.ManagedNetworkSpec) (substrate.ManagedNetworkInfo, error)
	AllowEgress(context.Context, string) error
	RemoveEgress(context.Context, string) error
}

// ManagedBridgeVerifier checks provider ownership independently of the kernel
// proof. Callers then pin and inspect the exported bridge incarnation.
type ManagedBridgeVerifier interface {
	VerifyManagedBridge(context.Context, substrate.ManagedBridgeTarget) error
	PreflightManagedBridge(context.Context) error
}

type GuestNetwork interface {
	EnsureRoute(context.Context, substrate.NodeHandle, string, string) error
	HasRoute(context.Context, substrate.NodeHandle, string, string) (bool, error)
}

// GuestNetworkInit runs before reset publishes the replacement for canonical
// guest-task routing. Implementations prepare host-side configuration and probe
// readiness; they must not start guest tasks requiring that published identity.
type GuestNetworkInit interface {
	PrepareGuestNetwork(context.Context, substrate.NodeHandle) error
	ObserveGuestNetwork(context.Context, substrate.NodeHandle) (substrate.GuestNetworkInitObservation, error)
}

type Reset interface {
	PrepareReset(context.Context, substrate.ResetRequest) (substrate.ResetHandle, error)
	DestroyReset(context.Context, substrate.ResetHandle) error
	ApplyReset(context.Context, substrate.ResetHandle) (substrate.NodeHandle, error)
	ObserveReset(context.Context, substrate.ResetHandle) (substrate.ResetObservation, error)
	CleanupReset(context.Context, substrate.ResetHandle) error
	MarshalResetHandle(substrate.ResetHandle) (json.RawMessage, error)
	UnmarshalResetHandle(json.RawMessage) (substrate.ResetHandle, error)
}

type Artifact interface {
	ResolveImage(context.Context, substrate.ArtifactSource) (substrate.ArtifactHandle, error)
}

type Import interface {
	ReadNode(context.Context, string) (substrate.NodeHandle, error)
}

type OwnershipAdoption interface {
	InspectAdoption(context.Context, string, substrate.NodeSpec) (substrate.AdoptionEvidence, error)
	ApplyAdoption(context.Context, substrate.AdoptionEvidence) (substrate.NodeHandle, error)
	ObserveAdoption(context.Context, substrate.AdoptionEvidence) (substrate.NodeHandle, error)
}

type NodeState interface {
	MarshalProviderState(substrate.NodeHandle) (json.RawMessage, error)
	UnmarshalProviderState(json.RawMessage) (any, error)
}

type ImageEntry interface {
	ExecImageEntry(context.Context, substrate.NodeHandle) error
}

type Power interface {
	Pause(context.Context, substrate.NodeHandle) error
	Resume(context.Context, substrate.NodeHandle) error
}

type IsolatedNetworkSpec struct {
	Name, Bridge, CIDR                string
	RootBridge, RootEnd, NamespaceEnd string
	RootAddress                       string
	RootRoutes                        []HostRouteSpec
}

// HostRouteSpec 描述通过 root netns 代理暴露的路由。它只属于 substrate 层，
// 不携带拓扑或产品语义，仅包含目的网段和下一跳地址。
type HostRouteSpec struct {
	Destination string
	Via         string
}
type LinuxNetwork interface {
	CreateIsolated(context.Context, IsolatedNetworkSpec) error
	DeleteIsolated(context.Context, IsolatedNetworkSpec) error
	NetworkHealthy(context.Context, IsolatedNetworkSpec) (bool, string)
	LinkHealthy(context.Context, string, string) bool
}

// IsolatedNetworkObserver makes absence an explicit successful point query.
// Any query or ownership ambiguity is an error, never Exists=false evidence.
type IsolatedNetworkObserver interface {
	ObserveIsolated(context.Context, IsolatedNetworkSpec) (IsolatedNetworkObservation, error)
}
type IsolatedNetworkObservation struct {
	Exists, Healthy bool
	Reason          string
}

type Descriptor struct {
	Name              string
	Version           string
	Node              Node
	NIC               NIC
	Snapshot          Snapshot
	GuestExec         GuestExec
	GuestFiles        GuestFiles
	Network           Network
	Artifact          Artifact
	Import            Import
	OwnershipAdoption OwnershipAdoption
	NodeState         NodeState
	ImageEntry        ImageEntry
	Power             Power
	Policy            Policy
	LinuxNetwork      LinuxNetwork
	GuestNetwork      GuestNetwork
	GuestNetworkInit  GuestNetworkInit
	Reset             Reset
}

func (d Descriptor) capability(capability Capability) any {
	switch capability {
	case CapabilityNode:
		return d.Node
	case CapabilityNIC:
		return d.NIC
	case CapabilitySnapshot:
		return d.Snapshot
	case CapabilityGuestExec:
		return d.GuestExec
	case CapabilityGuestFiles:
		return d.GuestFiles
	case CapabilityNetwork:
		return d.Network
	case CapabilityArtifact:
		return d.Artifact
	case CapabilityImport:
		return d.Import
	case CapabilityOwnershipAdoption:
		return d.OwnershipAdoption
	case CapabilityNodeState:
		return d.NodeState
	case CapabilityImageEntry:
		return d.ImageEntry
	case CapabilityPower:
		return d.Power
	case CapabilityLinuxNetwork:
		return d.LinuxNetwork
	case CapabilityGuestNetwork:
		return d.GuestNetwork
	case CapabilityGuestNetworkInit:
		return d.GuestNetworkInit
	case CapabilityReset:
		return d.Reset
	case CapabilityPolicy:
		return d.Policy
	default:
		return nil
	}
}

// ManagedAttachmentDiscovery returns the complete generation inventory keyed by
// logical attachment name. It must reject unaccounted attachments and uncertain
// ownership; it never creates, repairs or infers successful guest configuration.
type ManagedAttachmentDiscovery interface {
	DiscoverManagedAttachments(context.Context, substrate.NodeHandle, []AttachmentRequest) (map[string]AttachmentResult, error)
}
