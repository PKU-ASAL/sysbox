package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/oslab/sysbox/pkg/substrate"
)

// PowerDispatcher exposes only fixed pause/resume operations. An acknowledged
// synchronous provider call and a matching observation are both required before
// sealing its grant. Ambiguous daemon calls remain unresolved, even if a later
// observation happens to show the desired state.
type PowerDispatcher struct {
	Root, AgentID, HostBootID string
	IssuerKey                 ed25519.PublicKey
	HostKey                   ed25519.PrivateKey
	Resolve                   func(context.Context, TargetRef) (NodeTarget, error)
	Authorize                 func(context.Context, controlplane.SignedExecutionGrant) error
	ReportStop                func(context.Context, controlplane.SignedExecutionStopReceipt) error
}

func (d *PowerDispatcher) gate(g controlplane.SignedExecutionGrant) (*effectgate.Gate, error) {
	if d.AgentID == "" || d.HostBootID == "" || len(d.HostKey) != ed25519.PrivateKeySize || len(d.IssuerKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("host power supervisor not configured")
	}
	if err := EnsureOwnedDirectory(d.Root, 0700); err != nil {
		return nil, err
	}
	return &effectgate.Gate{Root: d.Root, PublicKey: d.IssuerKey, BootID: d.HostBootID, Authorize: func(ctx context.Context, _ controlplane.ExecutionGrant) error {
		if d.Authorize == nil {
			return effectgate.ErrFenced
		}
		return d.Authorize(ctx, g)
	}}, nil
}
func (d *PowerDispatcher) target(ctx context.Context, ref TargetRef) (NodeTarget, error) {
	if d.Resolve == nil {
		return NodeTarget{}, effectgate.ErrFenced
	}
	t, err := d.Resolve(ctx, ref)
	if err != nil {
		return t, err
	}
	if t.Scope != ref.Scope || t.AgentID != d.AgentID || t.Handle.ID == "" || t.Handle.Generation != ref.Scope.Generation || t.Node == nil || t.Power == nil {
		return t, effectgate.ErrFenced
	}
	return t, nil
}
func (d *PowerDispatcher) BootID(ctx context.Context, ref TargetRef) (string, error) {
	if _, err := d.target(ctx, ref); err != nil {
		return "", err
	}
	return d.HostBootID, nil
}
func (d *PowerDispatcher) proof(ref TargetRef, g controlplane.SignedExecutionGrant, now time.Time) error {
	if g.Grant.Operation != "node_power" || g.Grant.AgentID != d.AgentID || g.Grant.Scope != ref.Scope || g.Grant.TargetBootID == "" {
		return effectgate.ErrFenced
	}
	return controlplane.VerifyExecutionGrant(d.IssuerKey, g, g.BootID, now)
}
func (d *PowerDispatcher) Execute(ctx context.Context, ref TargetRef, req controlplane.NodePowerRequest, g controlplane.SignedExecutionGrant) error {
	if err := d.proof(ref, g, time.Now()); err != nil {
		return err
	}
	if g.BootID != d.HostBootID {
		return effectgate.ErrFenced
	}
	digest, err := req.Digest()
	if err != nil {
		return err
	}
	if req.Topology != ref.Topology || req.Node != ref.Node || digest != g.Grant.Digest {
		return effectgate.ErrFenced
	}
	gate, err := d.gate(g)
	if err != nil {
		return err
	}
	_, err = gate.Execute(ctx, g, g.Grant.ActionID, func(effectCtx context.Context) (json.RawMessage, error) {
		target, err := d.target(effectCtx, ref)
		if err != nil {
			return nil, err
		}
		// Provider discovery may block. Do not let the authorization checked
		// before discovery authorize a newly started effect after revocation.
		if err := effectCtx.Err(); err != nil {
			return nil, err
		}
		if err := d.Authorize(effectCtx, g); err != nil {
			return nil, err
		}
		identity := substrate.OperationIdentity{TopologyUID: ref.Scope.TopologyUID, OwnerUID: ref.Scope.TopologyUID, ResourceUID: ref.Scope.ResourceUID, Generation: ref.Scope.Generation, ActionID: g.Grant.ActionID}
		effectCtx = substrate.WithOperationIdentity(effectCtx, identity)
		if req.Operation == "pause" {
			err = target.Power.Pause(effectCtx, target.Handle)
		} else {
			err = target.Power.Resume(effectCtx, target.Handle)
		}
		if err != nil {
			return nil, err
		}
		observed, err := target.Node.ObserveNode(effectCtx, target.Handle)
		if err != nil {
			return nil, err
		}
		want := substrate.NodeStatusPaused
		if req.Operation == "resume" {
			want = substrate.NodeStatusRunning
		}
		if !observed.Exists || substrate.ValidateNodeObservationIdentity(identity, observed) != nil || observed.Status != want {
			return nil, fmt.Errorf("power outcome not confirmed by provider observation")
		}
		return json.Marshal(req)
	})
	if err != nil {
		return err
	}
	return d.finish(ctx, gate, g.Grant)
}
func (d *PowerDispatcher) sign(g controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error) {
	if g.AgentID != d.AgentID || g.Operation != "node_power" {
		return controlplane.SignedExecutionStopReceipt{}, effectgate.ErrFenced
	}
	return controlplane.SignExecutionStopReceipt(d.HostKey, g, d.HostBootID, rand.Text(), time.Now())
}
func (d *PowerDispatcher) finish(ctx context.Context, gate *effectgate.Gate, g controlplane.ExecutionGrant) error {
	if _, err := gate.SealStop(ctx, g, g.ActionID, d.sign); err != nil {
		return err
	}
	if d.ReportStop == nil {
		return fmt.Errorf("host stop reporting unavailable")
	}
	return gate.ReportStop(ctx, g, g.ActionID, d.ReportStop)
}

// Observe reads the exact durable completion, not the node's current power
// state. It is safe after grant expiry or host reboot and never replays effects.
func (d *PowerDispatcher) Observe(ctx context.Context, ref TargetRef, g controlplane.SignedExecutionGrant) error {
	if err := d.proof(ref, g, g.IssuedAt); err != nil {
		return err
	}
	gate, err := d.gate(g)
	if err != nil {
		return err
	}
	raw, err := gate.ReadOutcome(ctx, g.Grant, g.Grant.ActionID)
	if err != nil {
		return err
	}
	var req controlplane.NodePowerRequest
	if json.Unmarshal(raw, &req) != nil || req.Topology != ref.Topology || req.Node != ref.Node {
		return effectgate.ErrFenced
	}
	digest, err := req.Digest()
	if err != nil || digest != g.Grant.Digest {
		return effectgate.ErrFenced
	}
	return d.finish(ctx, gate, g.Grant)
}
func (d *PowerDispatcher) Recover(ctx context.Context) error {
	gate, err := d.gate(controlplane.SignedExecutionGrant{})
	if err != nil {
		return err
	}
	if d.ReportStop == nil {
		return fmt.Errorf("host stop reporting unavailable")
	}
	return gate.RecoverStops(ctx, d.sign, d.ReportStop)
}
