package driver

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/substrate"
)

type Capability string

const (
	CapabilityNode      Capability = "node"
	CapabilityNIC       Capability = "nic"
	CapabilitySnapshot  Capability = "snapshot"
	CapabilityConsole   Capability = "console"
	CapabilityGuestExec Capability = "guest-exec"
	CapabilityNetwork   Capability = "network"
	CapabilityArtifact  Capability = "artifact"
	CapabilityImport    Capability = "import"
	CapabilityNodeState Capability = "node-state"
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
	AttachNIC(context.Context, substrate.NodeHandle, substrate.LinkRequest) (substrate.AttachedNIC, error)
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

type Descriptor struct {
	Name      string
	Version   string
	Node      Node
	NIC       NIC
	Snapshot  Snapshot
	Console   Console
	GuestExec GuestExec
	Network   Network
	Artifact  Artifact
	Import    Import
	NodeState NodeState
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
	default:
		return nil
	}
}
