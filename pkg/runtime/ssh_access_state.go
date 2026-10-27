package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type persistedSSHAccess struct {
	Version int                       `json:"version"`
	Node    string                    `json:"node"`
	Handle  substrate.SSHAccessHandle `json:"handle"`
	Config  substrate.SSHAccessConfig `json:"config"`
}

func sshDesired(n *graph.Node, user, action string) (string, substrate.SSHAccessMutation, error) {
	cfg, ok := n.Data.(*config.SSHAccessConfig)
	if !ok {
		return "", substrate.SSHAccessMutation{}, fmt.Errorf("invalid SSH access configuration")
	}
	ref, err := config.ResolveResourceAddress(cfg.Node, "sysbox_node")
	if err != nil {
		return "", substrate.SSHAccessMutation{}, err
	}
	req := substrate.SSHAccessMutation{ID: action, Config: substrate.SSHAccessConfig{User: user, AccessID: n.Address.Name, AuthorizedKeys: append([]string(nil), cfg.AuthorizedKeys...), BindIP: cfg.BindIP, Port: cfg.Port}}
	return ref.String(), req, req.Validate()
}
func sshNodeConnection(ctx context.Context, st *state.State, node, accessID string) (context.Context, substrate.ConnectionSSHAccess, substrate.SSHAccessQuery, error) {
	var query substrate.SSHAccessQuery
	ref, err := address.Parse(node)
	if err != nil || ref.Type != "sysbox_node" || st == nil || st.TopologyUID == "" {
		return ctx, nil, query, fmt.Errorf("invalid SSH node reference")
	}
	resource := st.FindResource(ref)
	if resource == nil || resource.OwnerUID != st.TopologyUID || resource.ResourceUID == "" || resource.Generation < 1 {
		return ctx, nil, query, fmt.Errorf("canonical SSH node identity unavailable")
	}
	user, err := substrate.CanonicalGuestUser(resource.Attributes["desired"])
	if err != nil || user == "" {
		return ctx, nil, query, fmt.Errorf("canonical SSH user unavailable")
	}
	nd, err := driver.DefaultRegistry.RequireNode(resource.Driver)
	if err != nil {
		return ctx, nil, query, err
	}
	codec, err := driver.DefaultRegistry.RequireNodeState(resource.Driver)
	if err != nil {
		return ctx, nil, query, err
	}
	handle, err := resource.ReconstructHandle(codec)
	if err != nil {
		return ctx, nil, query, err
	}
	hints, err := substrate.CanonicalConnectionHints(ctx, resource.Attributes["desired"])
	if err != nil {
		return ctx, nil, query, err
	}
	conn, err := nd.Connection(handle, hints)
	if err != nil {
		return ctx, nil, query, err
	}
	access, ok := conn.(substrate.ConnectionSSHAccess)
	if !ok {
		return ctx, nil, query, fmt.Errorf("%w: node lacks managed SSH protocol", ErrConvergenceUnsupported)
	}
	action := "observe"
	if id, ok := substrate.OperationIdentityFromContext(ctx); ok {
		action = id.ActionID
	}
	ctx = substrate.WithOperationIdentity(ctx, substrate.OperationIdentity{ActionID: action, OwnerUID: st.TopologyUID, TopologyUID: st.TopologyUID, ResourceUID: resource.ResourceUID, Generation: resource.Generation})
	query = substrate.SSHAccessQuery{NodeID: handle.ID, User: user, AccessID: accessID, TopologyUID: st.TopologyUID, ResourceUID: resource.ResourceUID, Generation: resource.Generation}
	return ctx, access, query, query.Validate()
}
func decodeSSHState(current state.Resource, st *state.State) (persistedSSHAccess, error) {
	var saved persistedSSHAccess
	if st == nil || current.Address.Type != "sysbox_ssh_access" || current.OwnerUID == "" || current.OwnerUID != st.TopologyUID || current.ResourceUID == "" || current.Generation < 1 || current.SchemaVersion != 2 {
		return saved, fmt.Errorf("%w: managed SSH identity mismatch; old state requires rebuild", ErrConvergenceUnsupported)
	}
	raw, err := json.Marshal(current.Attributes["ssh_access"])
	if err != nil {
		return saved, err
	}
	if json.Unmarshal(raw, &saved) != nil || saved.Version != 1 || saved.Handle.Revision == 0 || saved.Handle.AccessID != current.Address.Name || saved.Handle.TopologyUID != st.TopologyUID || saved.Handle.BootID == "" || saved.Handle.Epoch == 0 {
		return saved, fmt.Errorf("invalid versioned SSH state")
	}
	request := substrate.SSHAccessMutation{ID: saved.Handle.ID, ExpectedRevision: saved.Handle.Revision - 1, Config: saved.Config}
	digest, err := request.Digest()
	if err != nil || digest != saved.Handle.Digest || saved.Config.User != saved.Handle.User || saved.Config.AccessID != saved.Handle.AccessID {
		return saved, fmt.Errorf("SSH state configuration digest mismatch")
	}
	return saved, nil
}
func validateSSHStateTarget(saved persistedSSHAccess, q substrate.SSHAccessQuery) error {
	h := saved.Handle
	if h.NodeID != q.NodeID || h.TopologyUID != q.TopologyUID || h.ResourceUID != q.ResourceUID || h.Generation != q.Generation || h.User != q.User || h.AccessID != q.AccessID {
		return fmt.Errorf("SSH node incarnation or account changed")
	}
	return nil
}
func validateSSHMutationResult(q substrate.SSHAccessQuery, req substrate.SSHAccessMutation, result substrate.SSHAccessResult) error {
	h := result.Handle
	digest, err := req.Digest()
	if err != nil || h.ID != req.ID || h.Digest != digest || h.User != req.Config.User || h.AccessID != req.Config.AccessID || h.Revision != req.ExpectedRevision+1 || h.BootID == "" || h.Epoch == 0 || result.Present == req.Delete {
		return fmt.Errorf("SSH mutation returned mismatched committed evidence")
	}
	return validateSSHStateTarget(persistedSSHAccess{Handle: h}, q)
}
func sshResource(n *graph.Node, id substrate.OperationIdentity, node string, result substrate.SSHAccessResult, cfg substrate.SSHAccessConfig) (state.Resource, error) {
	attrs := map[string]any{"ssh_access": persistedSSHAccess{Version: 1, Node: node, Handle: result.Handle, Config: cfg}}
	if err := setDesiredHash(n, attrs); err != nil {
		return state.Resource{}, err
	}
	return state.Resource{Address: n.Address, ResourceType: n.Address.Type, Driver: "managed", SchemaVersion: 2, OwnerUID: id.OwnerUID, ResourceUID: id.ResourceUID, Generation: id.Generation, ExternalID: result.Handle.AccessID, Status: state.ResourcePresent, Attributes: state.MustAttributes(attrs)}, nil
}
