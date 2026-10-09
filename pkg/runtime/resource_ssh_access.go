package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// -- sysbox_ssh_access --

type SSHAccessResourceHandler struct{}

func init() {
	RegisterResourceHandler(SSHAccessResourceHandler{})
}

func (SSHAccessResourceHandler) Type() string { return "sysbox_ssh_access" }

func (SSHAccessResourceHandler) Schema() ResourceSchema {
	return ResourceSchemaFor("sysbox_ssh_access")
}

func (SSHAccessResourceHandler) Read(_ context.Context, current state.Resource) (ResourceReadResult, error) {
	return ResourceReadResult{Status: state.ResourceUnknown, Resource: current}, fmt.Errorf("%w: ssh_access read requires host connection context", ErrConvergenceUnsupported)
}

func (SSHAccessResourceHandler) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlannedChange, error) {
	if desired == nil {
		return controlplane.PlannedChange{}, fmt.Errorf("ssh_access plan requires desired config")
	}
	cfg, ok := desired.Data.(*config.SSHAccessConfig)
	if !ok {
		return controlplane.PlannedChange{}, fmt.Errorf("ssh_access plan has invalid config")
	}
	keys := append([]string(nil), cfg.AuthorizedKeys...)
	nodeRef, err := config.ResolveResourceAddress(cfg.Node, "sysbox_node")
	if err != nil {
		return controlplane.PlannedChange{}, err
	}
	req := controlplane.SSHAccessRequest{ID: desired.Address.Name, Topology: "topology", Node: nodeRef.Name, AuthorizedKeys: keys, BindIP: cfg.BindIP, Port: cfg.Port}
	if err := req.Validate(); err != nil {
		return controlplane.PlannedChange{}, err
	}
	return planDiffByDesiredHash(desired, current)
}

func (SSHAccessResourceHandler) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	if pc == nil || pc.exec == nil || n == nil {
		return state.Resource{}, fmt.Errorf("%w: ssh_access requires a managed guest configuration contract", ErrConvergenceUnsupported)
	}
	cfg, ok := n.Data.(*config.SSHAccessConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("invalid ssh_access config")
	}
	nodeRef, err := config.ResolveResourceAddress(cfg.Node, "sysbox_node")
	if err != nil {
		return state.Resource{}, err
	}
	req := controlplane.SSHAccessRequest{ID: n.Address.Name, Topology: pc.Topology(), Node: nodeRef.Name, AuthorizedKeys: append([]string(nil), cfg.AuthorizedKeys...), BindIP: cfg.BindIP, Port: cfg.Port}
	if err := req.Validate(); err != nil {
		return state.Resource{}, err
	}
	conn, err := pc.ConnectionForNode(ctx, cfg.Node)
	if err != nil {
		return state.Resource{}, err
	}
	files, ok := conn.(substrate.ConnectionFileCopier)
	if !ok {
		return state.Resource{}, fmt.Errorf("%w: node has no managed file installer", ErrConvergenceUnsupported)
	}
	content, err := req.AuthorizedKeysContent()
	if err != nil {
		return state.Resource{}, err
	}
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	tmp, err := os.CreateTemp("", "sysbox-ssh-")
	if err != nil {
		return state.Resource{}, err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return state.Resource{}, err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return state.Resource{}, err
	}
	if err := tmp.Close(); err != nil {
		return state.Resource{}, err
	}
	user, err := pc.SSHUserForNode(cfg.Node)
	if err != nil {
		return state.Resource{}, err
	}
	if strings.ContainsAny(user, "/\\\x00") || user == "." || user == ".." {
		return state.Resource{}, fmt.Errorf("invalid SSH user")
	}
	path := filepath.Join("/home", user, ".ssh", "authorized_keys.d", "sysbox-"+req.ID)
	if err := files.CopyFile(ctx, name, path); err != nil {
		return state.Resource{}, fmt.Errorf("install managed ssh key: %w", err)
	}
	if execer, ok := conn.(interface {
		Exec(context.Context, substrate.ExecRequest, io.Writer, io.Writer) (substrate.ExecResult, error)
	}); ok {
		result, err := execer.Exec(ctx, substrate.ExecRequest{Program: "/bin/cat", Args: []string{path}, Shell: substrate.ShellNone}, nil, nil)
		if err != nil || !result.ExitCodeKnown || result.ExitCode != 0 || result.Stdout != string(content) {
			return state.Resource{}, fmt.Errorf("ssh access read-back failed")
		}
	}
	return state.Resource{Address: n.Address, ResourceType: n.Address.Type, Driver: "managed", OwnerUID: pc.State().TopologyUID, ResourceUID: pc.State().TopologyUID + "/" + n.Address.Name, Generation: 1, Status: state.ResourcePresent, Attributes: state.MustAttributes(map[string]any{"node": cfg.Node, "path": path, "sha256": digest, "authorized_keys": req.AuthorizedKeys})}, nil
}

func (SSHAccessResourceHandler) Delete(ctx context.Context, pc *ProviderContext, current state.Resource) error {
	node := current.Str("node")
	path := current.Str("path")
	if node == "" || path == "" || filepath.Clean(path) != path || !strings.Contains(path, "/.ssh/authorized_keys.d/sysbox-") {
		return fmt.Errorf("%w: invalid managed ssh access state", ErrConvergenceUnsupported)
	}
	conn, err := pc.ConnectionForNode(ctx, node)
	if err != nil {
		return err
	}
	execer, ok := conn.(interface {
		Exec(context.Context, substrate.ExecRequest, io.Writer, io.Writer) (substrate.ExecResult, error)
	})
	if !ok {
		return fmt.Errorf("%w: node has no managed command verifier", ErrConvergenceUnsupported)
	}
	result, err := execer.Exec(ctx, substrate.ExecRequest{Program: "/bin/rm", Args: []string{"-f", path}, Shell: substrate.ShellNone}, nil, nil)
	if err != nil || !result.ExitCodeKnown || result.ExitCode != 0 {
		return fmt.Errorf("delete managed ssh key: %w", err)
	}
	return nil
}

func (SSHAccessResourceHandler) ExternalID(current state.Resource) string {
	if id := current.ContainerID(); id != "" {
		return id
	}
	return current.Str("id")
}

func (SSHAccessResourceHandler) DecodeResource(r config.ResourceBlock, _ string, ctx *hcl.EvalContext) (any, []address.Address, error) {
	cfg := &config.SSHAccessConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return nil, nil, err
	}
	var deps []address.Address
	if cfg.Node != "" {
		ref, err := config.ResolveResourceAddress(cfg.Node, "sysbox_node")
		if err != nil {
			return nil, nil, err
		}
		deps = append(deps, ref)
	}
	return cfg, deps, nil
}
