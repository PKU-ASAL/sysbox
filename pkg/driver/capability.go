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
	CapabilityNode          Capability = "node"
	CapabilityNIC           Capability = "nic"
	CapabilitySnapshot      Capability = "snapshot"
	CapabilityConsole       Capability = "console"
	CapabilityGuestExec     Capability = "guest-exec"
	CapabilityNetwork       Capability = "network"
	CapabilityArtifact      Capability = "artifact"
	CapabilityImport        Capability = "import"
	CapabilityNodeState     Capability = "node-state"
	CapabilityImageEntry    Capability = "image-entry"
	CapabilityPower         Capability = "power"
	CapabilityRouterNetwork Capability = "router-network"
	CapabilityLinuxNetwork  Capability = "linux-network"
	CapabilityGuestNetwork  Capability = "guest-network"
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

type NIC interface {
	Attach(context.Context, substrate.NodeHandle, AttachmentRequest) (AttachmentResult, error)
	Observe(context.Context, substrate.NodeHandle, AttachmentRequest, json.RawMessage) (AttachmentResult, error)
	Delete(context.Context, substrate.NodeHandle, AttachmentRequest, json.RawMessage) error
}

type AttachmentRequest struct {
	Name         string
	Network      address.Address
	MAC          string
	IPPrefixes   []string
	Gateway      string
	NetworkState json.RawMessage
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

type Console interface {
	OpenConsole(context.Context, substrate.NodeHandle, substrate.ConsoleRequest) (substrate.ConsoleSession, error)
}

type GuestExec interface {
	ExecInNode(context.Context, substrate.NodeHandle, substrate.ExecSpec) (substrate.ExecResult, error)
	ExecBackground(context.Context, substrate.NodeHandle, substrate.ExecSpec) (int, error)
}

type Network interface {
	CreateManagedNetwork(context.Context, substrate.ManagedNetworkSpec) (substrate.ManagedNetworkInfo, error)
	RemoveManagedNetwork(context.Context, string) error
	ReadManagedNetwork(context.Context, substrate.ManagedNetworkSpec) (substrate.ManagedNetworkInfo, error)
	AllowEgress(context.Context, string) error
	RemoveEgress(context.Context, string) error
}

type GuestNetwork interface {
	EnsureRoute(context.Context, substrate.NodeHandle, string, string) error
	HasRoute(context.Context, substrate.NodeHandle, string, string) (bool, error)
}

type Artifact interface {
	PrepareImage(context.Context, substrate.ImageSpec) (substrate.ImageRef, error)
}

type Import interface {
	ReadNode(context.Context, string) (substrate.NodeHandle, error)
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

type RouterNetwork interface {
	ConfigureNAT(context.Context, substrate.NodeHandle, AttachmentRequest, AttachmentResult, AttachmentRequest, AttachmentResult) error
}

type IsolatedNetworkSpec struct{ Name, Bridge, CIDR string }
type FirewallRule struct {
	Proto          string
	DPort          int
	SrcNet, Action string
}
type LinuxNetwork interface {
	CreateIsolated(context.Context, IsolatedNetworkSpec) error
	DeleteIsolated(context.Context, IsolatedNetworkSpec) error
	NetworkHealthy(context.Context, IsolatedNetworkSpec) (bool, string)
	LinkHealthy(context.Context, string, string) bool
	DeleteAttachment(context.Context, string, string, string) error
	ApplyFirewall(context.Context, string, []FirewallRule) error
	DeleteFirewall(context.Context, string) error
}

type Descriptor struct {
	Name          string
	Version       string
	Node          Node
	NIC           NIC
	Snapshot      Snapshot
	Console       Console
	GuestExec     GuestExec
	Network       Network
	Artifact      Artifact
	Import        Import
	NodeState     NodeState
	ImageEntry    ImageEntry
	Power         Power
	RouterNetwork RouterNetwork
	Policy        Policy
	LinuxNetwork  LinuxNetwork
	GuestNetwork  GuestNetwork
}

func (d Descriptor) capability(capability Capability) any {
	switch capability {
	case CapabilityNode:
		return d.Node
	case CapabilityNIC:
		return d.NIC
	case CapabilitySnapshot:
		return d.Snapshot
	case CapabilityConsole:
		return d.Console
	case CapabilityGuestExec:
		return d.GuestExec
	case CapabilityNetwork:
		return d.Network
	case CapabilityArtifact:
		return d.Artifact
	case CapabilityImport:
		return d.Import
	case CapabilityNodeState:
		return d.NodeState
	case CapabilityImageEntry:
		return d.ImageEntry
	case CapabilityPower:
		return d.Power
	case CapabilityRouterNetwork:
		return d.RouterNetwork
	case CapabilityLinuxNetwork:
		return d.LinuxNetwork
	case CapabilityGuestNetwork:
		return d.GuestNetwork
	default:
		return nil
	}
}
