package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/agentexec"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
)

// LocalSSHController embeds the production admission/authority service without
// listening on a socket. Its caller holds the canonical topology execution lock
// for construction, all mutations and Close. Keys are operator-provisioned.
type LocalSSHController struct {
	server          *Server
	host            *hostsupervisor.SSHGuestDispatcher
	resolver        hostsupervisor.CanonicalResolver
	authority       hostsupervisor.AuthorityClient
	topology, owner string
	agent           controlplane.Agent
	session         controlplane.AgentSession
}

func readLocalKey(path string, private bool) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("local SSH requires enrolled issuer, public and host keys in service config")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = hostsupervisor.ValidateOwnedPath(absolute, private); err != nil {
		return nil, err
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, ed25519.PrivateKeySize+1))
	if err != nil {
		return nil, err
	}
	size := ed25519.PublicKeySize
	if private {
		size = ed25519.PrivateKeySize
	}
	if len(raw) != size || private && !bytes.Equal(ed25519.NewKeyFromSeed(raw[:ed25519.SeedSize]), raw) {
		return nil, controlplane.ErrInvalidExecutionGrant
	}
	return raw, nil
}

func NewLocalSSHController(ctx context.Context, cfg config.ServiceConfig, topology string) (controller *LocalSSHController, resultErr error) {
	if err := validatePathSegment(topology, "topology"); err != nil {
		return nil, err
	}
	issuer, err := readLocalKey(cfg.API.ExecutionGrant.SigningKeyFile, true)
	if err != nil {
		return nil, err
	}
	public, err := readLocalKey(cfg.Agent.ExecutionGrantPublicKeyFile, false)
	if err != nil {
		return nil, err
	}
	host, err := readLocalKey(cfg.HostSupervisor.SigningKeyFile, true)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(ed25519.PrivateKey(issuer).Public().(ed25519.PublicKey), public) {
		return nil, fmt.Errorf("local issuer does not match enrolled guest public key")
	}
	c := &LocalSSHController{server: newServerWithConfig(cfg, false), topology: topology}
	defer func() {
		if resultErr != nil {
			_ = c.Close()
		}
	}()
	if c.server.initErr != nil {
		return nil, c.server.initErr
	}
	state, err := c.server.workspaceService().LoadState(topology)
	if err != nil {
		return nil, err
	}
	c.owner = state.TopologyUID
	if c.owner == "" {
		return nil, fmt.Errorf("local topology has no canonical owner")
	}
	hash := sha256.Sum256([]byte(c.owner))
	id := fmt.Sprintf("local-%x", hash[:12])
	placement, err := c.server.workspaceService().loadPlacement(topology)
	if err != nil {
		return nil, err
	}
	if placement != nil && placement.AgentID != id {
		return nil, fmt.Errorf("topology is assigned to another execution host: %s", placement.AgentID)
	}
	keyHash := sha256.Sum256(append(append([]byte(nil), public...), ed25519.PrivateKey(host).Public().(ed25519.PublicKey)...))
	binding := fmt.Sprintf("%x", keyHash[:])
	existing, err := c.server.apiStore.GetAgent(ctx, id)
	if err == nil {
		if existing.IsBlocked() || existing.Labels["local_ssh_keys"] != binding || existing.Labels["local_topology_uid"] != c.owner {
			return nil, fmt.Errorf("local execution identity/key binding conflict")
		}
		c.agent = *existing
	} else if errors.Is(err, persistence.ErrNotFound) {
		secret := rand.Text()
		c.agent = controlplane.Agent{ID: id, Status: "offline", Protocol: controlplane.AgentProtocolVersion, AuthSecret: secret, SecretHash: agent.SecretHash(secret), Labels: map[string]string{"local_ssh_keys": binding, "local_topology_uid": c.owner}}
		if err = c.server.apiStore.SaveAgent(ctx, c.agent); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	// Local controllers are not online workers and cannot attract remote Runs.
	c.agent.Status = "offline"
	c.server.grantHostKeys[id] = ed25519.PrivateKey(host).Public().(ed25519.PublicKey)
	root, err := filepath.Abs(filepath.Join(c.server.runsDir, "_local-control", id))
	if err != nil {
		return nil, err
	}
	if err = hostsupervisor.EnsureOwnedDirectory(root, 0700); err != nil {
		return nil, err
	}
	client := &http.Client{Transport: localAuthorityTransport{handler: c.server}, Timeout: 30 * time.Second}
	identity := agent.Identity{ID: id, Secret: c.agent.AuthSecret, APIURL: "http://local.sysbox", Token: cfg.API.Token}
	c.session, err = agentexec.OpenExecutionSession(ctx, agentexec.Options{ID: id, Secret: c.agent.AuthSecret, APIURL: identity.APIURL, Token: identity.Token, ExecutionSessionFile: filepath.Join(root, "session.json")}, client)
	if err != nil {
		return nil, err
	}
	authority := hostsupervisor.AuthorityClient{Identity: identity, HTTP: client}
	c.authority = authority
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, err
	}
	c.resolver = hostsupervisor.CanonicalResolver{RunsDir: c.server.runsDir, Backend: c.server.stateBackend, AgentID: id}
	c.host = &hostsupervisor.SSHGuestDispatcher{Dispatcher: &hostsupervisor.Dispatcher{Root: filepath.Join(root, "guest-ssh"), AgentID: id, HostBootID: strings.TrimSpace(string(boot)), IssuerKey: public, HostKey: host, Resolve: c.resolver.Resolve, Authorize: authority.Authorize}, Report: func(reportCtx context.Context, operation string, receipt controlplane.SignedExecutionStopReceipt, evidence json.RawMessage) error {
		if err := authority.ReportNodeOperation(reportCtx, operation, receipt, evidence); err != nil {
			return err
		}
		// The CLI has no delivery worker. Retire only after authenticated host
		// completion, keeping the command tombstone for exact receipt replay.
		return c.server.apiStore.DeleteAgentCommand(reportCtx, id, receipt.Grant.CommandID)
	}}
	if err = c.host.Recover(ctx); err != nil {
		return nil, fmt.Errorf("recover local SSH authority before new effects: %w", err)
	}
	if err = c.server.workspaceService().bindPlacement(topology, id, controlplane.AgentProtocolVersion); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *LocalSSHController) Context(ctx context.Context) context.Context {
	return transport.WithSSHAccessMutationHandler(ctx, c.mutate)
}

func (c *LocalSSHController) mutate(ctx context.Context, req substrate.SSHAccessMutation, id substrate.OperationIdentity) (substrate.SSHAccessResult, error) {
	var empty substrate.SSHAccessResult
	if id.Validate() != nil || id.OwnerUID != c.owner || id.TopologyUID != c.owner || id.ActionID != req.ID {
		return empty, fmt.Errorf("local SSH action identity mismatch")
	}
	state, err := c.server.workspaceService().LoadState(c.topology)
	if err != nil {
		return empty, err
	}
	if state.TopologyUID != c.owner {
		return empty, persistence.ErrConflict
	}
	var node string
	for _, resource := range state.Resources {
		if resource.Address.Type == "sysbox_node" && resource.OwnerUID == id.OwnerUID && resource.ResourceUID == id.ResourceUID && resource.Generation == id.Generation {
			if node != "" {
				return empty, persistence.ErrConflict
			}
			node = resource.Address.Name
		}
	}
	if node == "" {
		return empty, fmt.Errorf("local SSH node identity unavailable")
	}
	ref := hostsupervisor.TargetRef{Topology: c.topology, Node: node, Scope: controlplane.ExecutionScope{TopologyUID: c.owner, ResourceUID: id.ResourceUID, Generation: id.Generation}}
	target, err := c.resolver.Resolve(ctx, ref)
	if err != nil {
		return empty, err
	}
	if target.User != req.Config.User {
		return empty, persistence.ErrConflict
	}
	found, err := c.host.DiscoverSSHAccess(ctx, ref, req.Config.AccessID)
	if err != nil {
		return empty, err
	}
	operation := "ssh_access_apply"
	if req.Delete {
		operation = "ssh_access_delete"
	}
	request := controlplane.SSHAccessRequest{ID: req.ID, Topology: c.topology, Node: node, AccessID: req.Config.AccessID, ExpectedRevision: req.ExpectedRevision, AuthorizedKeys: req.Config.AuthorizedKeys, BindIP: req.Config.BindIP, Port: req.Config.Port}
	// The same canonical validation and atomic operation/outbox admission as HTTP
	// is used here; only host selection is explicit for this local operator.
	service := newNodeOperationService(c.server.workspaceService(), c.server.scheduling(), c.server.nodeOps, nil)
	op, err := service.sshAccess(ctx, c.topology, node, request, operation, requestSubject{User: "local-cli", Roles: []string{"admin"}}, &c.agent)
	if err != nil {
		return empty, err
	}
	command, ok, err := c.server.apiStore.AcquireAgentCommandLease(ctx, c.agent.ID, op.CommandID, c.session.ID, time.Minute)
	if err != nil {
		return empty, err
	}
	if !ok {
		return empty, persistence.ErrConflict
	}
	grant, err := c.authority.IssueCommandGrant(ctx, *command, c.session.ID, found.BootID)
	if err != nil {
		return empty, err
	}
	result, err := c.host.Mutate(ctx, ref, req, grant)
	if err != nil {
		return result, err
	}
	result.Handle.NodeID = target.NodeID
	return result, nil
}

func (c *LocalSSHController) Close() error {
	if c == nil || c.server == nil {
		return nil
	}
	var failures []error
	for _, repo := range c.server.repositories {
		failures = append(failures, repo.Close())
	}
	switch store := c.server.apiStore.(type) {
	case *sqliteAPIStore:
		if store.db != nil {
			failures = append(failures, store.db.Close())
		}
	case *postgresAPIStore:
		if store.pool != nil {
			store.pool.Close()
		}
	}
	c.server = nil
	return errors.Join(failures...)
}

type localAuthorityTransport struct{ handler http.Handler }

func (t localAuthorityTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "http" || req.URL.Host != "local.sysbox" {
		return nil, fmt.Errorf("local authority transport cannot contact an external endpoint")
	}
	response := &localAuthorityResponse{header: make(http.Header)}
	t.handler.ServeHTTP(response, req)
	if response.status == 0 {
		response.status = http.StatusOK
	}
	return &http.Response{StatusCode: response.status, Header: response.header, Body: io.NopCloser(bytes.NewReader(response.body.Bytes())), Request: req}, nil
}

type localAuthorityResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *localAuthorityResponse) Header() http.Header { return r.header }
func (r *localAuthorityResponse) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}
func (r *localAuthorityResponse) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(body)
}
