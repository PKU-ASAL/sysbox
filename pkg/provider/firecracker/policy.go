package firecracker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
)

type firecrackerPolicyTarget struct {
	ContainerID   string            `json:"container_id"`
	ProviderState json.RawMessage   `json:"provider_state"`
	Bindings      map[string]string `json:"bindings"`
}

func (s *Substrate) policyConnection(ctx context.Context, target driver.PolicyTarget) (*policyGuestConnection, map[string]string, error) {
	if err := target.Validate(); err != nil {
		return nil, nil, err
	}
	var value firecrackerPolicyTarget
	if err := json.Unmarshal(target.State, &value); err != nil {
		return nil, nil, err
	}
	var supplied HandleState
	if err := json.Unmarshal(value.ProviderState, &supplied); err != nil {
		return nil, nil, err
	}
	h := substrate.NodeHandle{ID: value.ContainerID, Generation: target.Identity.Generation, Provider: &supplied}
	m, err := s.loadManifest(ctx, h)
	if err != nil {
		return nil, nil, err
	}
	if m.OwnerUID != target.Identity.OwnerUID || m.TopologyUID != target.Identity.TopologyUID || m.ResourceUID != target.Identity.ResourceUID || m.Generation != target.Identity.Generation || m.Handle.VsockUDS == "" {
		return nil, nil, driver.Wrap(driver.ErrorInvalidState, "firecracker", "policy target does not match canonical VM", nil)
	}
	// Use canonical channel coordinates, never caller-supplied socket paths.
	conn := transport.NewVsockConnection(m.Handle.VsockUDS, m.Handle.VsockPort)
	conn.BindExecutionIdentity(m.VMID, m.Generation)
	return &policyGuestConnection{VsockConnection: conn, nodeID: m.VMID, generation: m.Generation}, value.Bindings, nil
}
func policyRequest(target driver.PolicyTarget, op, owner string, spec driver.RulesetSpec, bindings map[string]string) network.GuestPolicyRequest {
	return network.GuestPolicyRequest{Operation: op, Node: target.Identity, Identity: target.Policy, Owner: owner, Spec: spec, Bindings: bindings}
}
func (s *Substrate) ApplyRuleset(ctx context.Context, target driver.PolicyTarget, spec driver.RulesetSpec) (driver.RulesetObservation, error) {
	conn, bindings, err := s.policyConnection(ctx, target)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	req := policyRequest(target, "apply", spec.Owner, spec, bindings)
	if err = executeGuestPolicy(ctx, conn, req); err != nil {
		return driver.RulesetObservation{}, err
	}
	req.Operation = "discover"
	return conn.ObserveGuestPolicy(ctx, req)
}
func (s *Substrate) ObserveRuleset(ctx context.Context, target driver.PolicyTarget, owner string) (driver.RulesetObservation, error) {
	conn, bindings, err := s.policyConnection(ctx, target)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	return conn.ObserveGuestPolicy(ctx, policyRequest(target, "observe", owner, driver.RulesetSpec{}, bindings))
}
func (s *Substrate) DiscoverRuleset(ctx context.Context, target driver.PolicyTarget, spec driver.RulesetSpec) (driver.RulesetObservation, error) {
	conn, bindings, err := s.policyConnection(ctx, target)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	return conn.ObserveGuestPolicy(ctx, policyRequest(target, "discover", spec.Owner, spec, bindings))
}
func (s *Substrate) DeleteRuleset(ctx context.Context, target driver.PolicyTarget, owner string) error {
	conn, bindings, err := s.policyConnection(ctx, target)
	if err != nil {
		return err
	}
	return executeGuestPolicy(ctx, conn, policyRequest(target, "delete", owner, driver.RulesetSpec{}, bindings))
}

type policyTaskConnection interface {
	GuestTaskBootID(context.Context) (string, error)
	Start(context.Context, substrate.ExecRequest) (substrate.ExecutionHandle, error)
	ObserveExecution(context.Context, substrate.ExecutionHandle) (substrate.ExecResult, error)
}

func executeGuestPolicy(ctx context.Context, conn policyTaskConnection, request network.GuestPolicyRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	boot, err := conn.GuestTaskBootID(ctx)
	if err != nil {
		return err
	}
	if boot == "" {
		return guesttask.ErrUnknown
	}
	node := request.Node
	incarnation := boot
	if parent, ok := transport.ParentExecutionGrantFromContext(ctx); ok {
		incarnation = fmt.Sprintf("%s/%s/%d/%d", boot, parent.Grant.CommandID, parent.Grant.Attempt, parent.Grant.Epoch)
	}
	node.ActionID = fmt.Sprintf("%s-%s-%x", request.Identity.ActionID, request.Operation, sha256.Sum256(append(raw, []byte("\x00"+incarnation)...)))
	ctx = substrate.WithOperationIdentity(ctx, node)
	h, err := conn.Start(ctx, substrate.ExecRequest{Program: guesttask.PolicyProgram, Shell: substrate.ShellNone, Stdin: bytes.NewReader(raw), OutputLimitBytes: 4096})
	if err != nil {
		return err
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		result, err := conn.ObserveExecution(ctx, h)
		if err != nil {
			return err
		}
		if result.RemoteTermination == substrate.RemoteTerminationConfirmed {
			if !result.ExitCodeKnown || result.ExitCode != 0 {
				detail := ""
				if output, ok := conn.(substrate.ConnectionExecutionOutput); ok {
					if stream, readErr := output.ReadExecutionOutput(ctx, h, "stderr", 0, 4096); readErr == nil {
						detail = string(stream.Data)
					}
				}
				return fmt.Errorf("guest policy task did not complete successfully (exit=%d known=%t): %s", result.ExitCode, result.ExitCodeKnown, detail)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

var _ driver.Policy = (*Substrate)(nil)
var _ driver.PolicyDiscovery = (*Substrate)(nil)

// RunChildren journals logical node names; the provider channel uses the
// canonical external VM identity. Translate only the query handle, retaining
// the host journal and every signed scope/epoch/digest field unchanged.
type policyGuestConnection struct {
	*transport.VsockConnection
	nodeID     string
	generation int64
}

func (c *policyGuestConnection) Start(ctx context.Context, req substrate.ExecRequest) (substrate.ExecutionHandle, error) {
	h, err := c.VsockConnection.Start(ctx, req)
	if err != nil {
		return h, err
	}
	if h.Generation != c.generation {
		return h, guesttask.ErrConflict
	}
	h.NodeID = c.nodeID
	return h, nil
}
