package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ConfigureExecutionGrantSigner must be called before serving. The issuer key
// is operator-provisioned and never generated per API restart or sent to workers.
func (s *Server) ConfigureExecutionGrantSigner(key ed25519.PrivateKey) error {
	if len(key) != ed25519.PrivateKeySize || !bytes.Equal(ed25519.NewKeyFromSeed(key[:ed25519.SeedSize]), key) {
		return controlplane.ErrInvalidExecutionGrant
	}
	s.grantSigner = append(ed25519.PrivateKey(nil), key...)
	return nil
}

func (s *Server) loadExecutionGrantSigner() error {
	s.grantHostKeys = map[string]ed25519.PublicKey{}
	for agent, path := range s.cfg.API.ExecutionGrant.HostPublicKeyFiles {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("load execution host public key: %w", err)
		}
		if agent == "" || len(raw) != ed25519.PublicKeySize {
			return errors.New("invalid execution host public key")
		}
		s.grantHostKeys[agent] = ed25519.PublicKey(raw)
	}
	path := s.cfg.API.ExecutionGrant.SigningKeyFile
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open execution grant signing key: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("execution grant signing key must be a private regular file (0600)")
	}
	raw, err := io.ReadAll(io.LimitReader(f, ed25519.PrivateKeySize+1))
	if err != nil {
		return err
	}
	return s.ConfigureExecutionGrantSigner(ed25519.PrivateKey(raw))
}
func (s *Server) registerExecutionGrantRoutes() {
	s.mux.HandleFunc("POST /v1/agents/{agent}/execution-session", s.handleExecutionSession)
	s.mux.HandleFunc("POST /v1/agents/{agent}/commands/{command}/execution-grant", s.handleExecutionGrant)
	s.mux.HandleFunc("POST /v1/agents/{agent}/commands/{command}/execution-grant/authorize", s.handleAuthorizeExecutionGrant)
	s.mux.HandleFunc("POST /v1/agents/{agent}/commands/{command}/execution-grant/renew", s.handleAuthorizeExecutionGrant)
	s.mux.HandleFunc("POST /v1/agents/{agent}/execution-grant/derive-guest", s.handleDeriveGuestExecutionGrant)
	s.mux.HandleFunc("POST /v1/agents/{agent}/gc-grant", s.handleGCGrant)
	s.mux.HandleFunc("POST /v1/agents/{agent}/execution-grants/revoke", s.handleRevokeExecutionGrant)
	s.mux.HandleFunc("POST /v1/agents/{agent}/execution-grants/inspect", s.handleInspectExecutionGrant)
	s.mux.HandleFunc("POST /v1/agents/{agent}/execution-grants/authorize", s.handleHostAuthorizeExecutionGrant)
	s.mux.HandleFunc("POST /v1/hosts/{agent}/execution-grants/stopped", s.handleStoppedExecutionGrant)
	s.mux.HandleFunc("POST /v1/hosts/{agent}/runs/{run}/completed", s.handleHostRunCompletion)
	s.mux.HandleFunc("POST /v1/hosts/{agent}/node-operations/{operation}/completed", s.handleHostNodeOperationCompletion)
}

type gcGrantRequest struct {
	Candidate  hostsupervisor.GCCandidate `json:"candidate"`
	Topology   string                     `json:"topology"`
	SessionID  string                     `json:"session_id"`
	ActionID   string                     `json:"action_id"`
	CommandID  string                     `json:"command_id"`
	Attempt    int                        `json:"attempt"`
	LeaseOwner string                     `json:"lease_owner"`
	BootID     string                     `json:"boot_id"`
}

func (s *Server) handleGCGrant(w http.ResponseWriter, r *http.Request) {
	if err := s.authenticatedGrantAgent(r); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if len(s.grantSigner) != ed25519.PrivateKeySize {
		writeError(w, http.StatusServiceUnavailable, controlplane.ErrInvalidExecutionGrant)
		return
	}
	var request gcGrantRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.Topology == "" || request.SessionID == "" || request.ActionID == "" || request.CommandID == "" || request.Attempt < 1 || request.LeaseOwner == "" || request.BootID == "" {
		writeError(w, http.StatusBadRequest, persistence.ErrInvalidRecord)
		return
	}
	if err := request.Candidate.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	manager, err := s.stateManager(request.Topology)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	canonical, err := manager.LoadWithContext(r.Context())
	if err != nil || canonical == nil || canonical.TopologyUID != request.Candidate.TopologyUID {
		writeError(w, http.StatusConflict, persistence.ErrConflict)
		return
	}
	if canonical.Meta.Serial < 0 {
		writeError(w, http.StatusConflict, persistence.ErrConflict)
		return
	}
	digest, err := request.Candidate.Digest()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if request.Candidate.InventoryDigest == "" {
		writeError(w, http.StatusConflict, persistence.ErrConflict)
		return
	}
	grant := controlplane.ExecutionGrant{TargetBootID: request.BootID, Scope: controlplane.ExecutionScope{TopologyUID: request.Candidate.TopologyUID, ResourceUID: request.Candidate.ResourceUID, Generation: request.Candidate.Generation}, AgentID: r.PathValue("agent"), SessionID: request.SessionID, ActionID: request.ActionID, CommandID: request.CommandID, Attempt: uint64(request.Attempt), Operation: "gc_delete", Digest: digest, ExpiresAt: time.Now().UTC().Add(2 * time.Minute)}
	issued, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore}).Issue(r.Context(), grant, 2*time.Minute)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	signed, err := controlplane.SignExecutionGrant(s.grantSigner, *issued, request.BootID, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, signed)
}

type deriveGuestGrantRequest struct {
	Parent      controlplane.SignedExecutionGrant `json:"parent"`
	TaskID      string                            `json:"task_id"`
	TaskDigest  string                            `json:"task_digest"`
	ResourceUID string                            `json:"resource_uid"`
	Generation  int64                             `json:"generation"`
	GuestBootID string                            `json:"guest_boot_id"`
}

func (s *Server) handleDeriveGuestExecutionGrant(w http.ResponseWriter, r *http.Request) {
	if err := s.authenticatedGrantAgent(r); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if len(s.grantSigner) != ed25519.PrivateKeySize {
		writeError(w, http.StatusServiceUnavailable, controlplane.ErrInvalidExecutionGrant)
		return
	}
	var request deriveGuestGrantRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.TaskID == "" || request.TaskDigest == "" || request.ResourceUID == "" || request.Generation < 1 || request.GuestBootID == "" {
		writeError(w, http.StatusBadRequest, persistence.ErrInvalidRecord)
		return
	}
	parent := request.Parent
	if parent.Grant.AgentID != r.PathValue("agent") || controlplane.VerifyExecutionGrant(s.grantSigner.Public().(ed25519.PublicKey), parent, parent.BootID, time.Now()) != nil {
		writeError(w, http.StatusConflict, controlplane.ErrInvalidExecutionGrant)
		return
	}
	stored, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore}).InspectGrant(r.Context(), parent.Grant.AgentID, parent.Grant.Scope)
	if err != nil || stored == nil || stored.Epoch != parent.Grant.Epoch || stored.State != controlplane.GrantActive || stored.Digest != parent.Grant.Digest {
		writeError(w, http.StatusConflict, persistence.ErrConflict)
		return
	}
	child := controlplane.ExecutionGrant{TargetBootID: request.GuestBootID, Scope: controlplane.ExecutionScope{TopologyUID: parent.Grant.Scope.TopologyUID, ResourceUID: request.ResourceUID, Generation: request.Generation}, AgentID: parent.Grant.AgentID, SessionID: parent.Grant.SessionID, ActionID: request.TaskID, CommandID: parent.Grant.CommandID, Attempt: parent.Grant.Attempt, Operation: "guest_task", Digest: request.TaskDigest, ExpiresAt: parent.Grant.ExpiresAt}
	ttl := time.Until(parent.Grant.ExpiresAt)
	derived, err := mustDeriveGuestGrant(r.Context(), apiExecutionGrantStorage{store: s.apiStore}, parent.Grant, child, ttl)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	child = *derived
	signed, err := controlplane.SignExecutionGrant(s.grantSigner, child, request.GuestBootID, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, signed)
}

func mustDeriveGuestGrant(ctx context.Context, storage apiExecutionGrantStorage, parent, child controlplane.ExecutionGrant, ttl time.Duration) (*controlplane.ExecutionGrant, error) {
	var result *controlplane.ExecutionGrant
	err := storage.UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
		memory := &grantTransaction{raw: raw}
		derived, err := persistence.NewExecutionGrantRepository(memory).DeriveGuestTask(ctx, parent, child, ttl)
		if err == nil {
			result = derived
		}
		return memory.raw, err
	})
	return result, err
}

type hostRunCompletion struct {
	Receipt  controlplane.SignedExecutionStopReceipt `json:"receipt"`
	Evidence json.RawMessage                         `json:"evidence"`
}

// handleHostRunCompletion is the host-owned completion boundary for remote
// apply/destroy. The worker cannot manufacture a terminal Run: the signed host
// receipt, durable command identity and the current run lease must all agree.
func (s *Server) handleHostRunCompletion(w http.ResponseWriter, r *http.Request) {
	agentID, runID := r.PathValue("agent"), r.PathValue("run")
	if err := validatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validatePathSegment(runID, "run"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var body hostRunCompletion
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	key := s.grantHostKeys[agentID]
	grant := body.Receipt.Grant
	if grant.AgentID != agentID || grant.ActionID != runID || (grant.Operation != "run_apply" && grant.Operation != "run_destroy") || controlplane.VerifyExecutionStopReceipt(key, body.Receipt) != nil {
		writeError(w, http.StatusConflict, controlplane.ErrInvalidExecutionGrant)
		return
	}
	run, err := s.apiStore.GetRun(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	commands, err := s.apiStore.ListAgentCommands(r.Context(), agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var command *controlplane.AgentCommand
	for i := range commands {
		if commands[i].ID == grant.CommandID {
			command = &commands[i]
			break
		}
	}
	if command == nil || command.Type != "run_assigned" || command.Run == nil || command.Run.ID != runID || command.TopologyUID != grant.Scope.TopologyUID || command.ActionID != grant.ActionID || command.ResourceUID != grant.Scope.ResourceUID || command.Generation != grant.Scope.Generation {
		writeError(w, http.StatusConflict, persistence.ErrConflict)
		return
	}
	rawReceipt, err := json.Marshal(body.Receipt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	receiptHash := sha256.Sum256(rawReceipt)
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	if err := authority.ValidateHostStop(r.Context(), body.Receipt.Grant, hex.EncodeToString(receiptHash[:])); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	var observed controlplane.Run
	if err := json.Unmarshal(body.Evidence, &observed); err != nil || observed.ID != runID || !observed.Status.IsTerminal() {
		writeError(w, http.StatusConflict, persistence.ErrConflict)
		return
	}
	completion := controlplane.RunCompletion{Run: *run}
	completion.Run.Status = observed.Status
	completion.Run.Err = observed.Err
	completion.Run.EndedAt = observed.EndedAt
	completion.Run.Cleanup = observed.Cleanup
	completion.Cleanup = observed.Cleanup
	completed, err := s.agentService().CompleteRun(agentID, runID, completion)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if err := authority.AcceptHostStop(r.Context(), body.Receipt.Grant, hex.EncodeToString(receiptHash[:])); err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, completed)
}

// Host authorization consumes a signed existing grant. It never renews or
// mints authority, and derives the dispatch lease from the locked source row.
func (s *Server) handleHostAuthorizeExecutionGrant(w http.ResponseWriter, r *http.Request) {
	if err := s.authenticatedGrantAgent(r); err != nil {
		writeError(w, 401, err)
		return
	}
	if len(s.grantSigner) != ed25519.PrivateKeySize {
		writeError(w, 503, controlplane.ErrInvalidExecutionGrant)
		return
	}
	var signed controlplane.SignedExecutionGrant
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	if err := d.Decode(&signed); err != nil {
		writeError(w, 400, err)
		return
	}
	base := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	if err := base.ObserveTime(r.Context()); err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if err := controlplane.VerifyExecutionGrant(s.grantSigner.Public().(ed25519.PublicKey), signed, signed.BootID, time.Now()); err != nil {
		writeError(w, 409, err)
		return
	}
	g := signed.Grant
	storage := apiExecutionGrantStorage{store: s.apiStore, commandID: g.CommandID, validate: func(record *persistence.CommandRecord) error {
		if record == nil {
			return persistence.ErrConflict
		}
		proof := ExecutionGrantRequest{SessionID: g.SessionID, LeaseOwner: record.Command.LeaseOwner, Attempt: int(g.Attempt), BootID: signed.BootID}
		current, err := s.sourceGrant(r.Context(), r.PathValue("agent"), proof, record)
		if err != nil {
			return err
		}
		if current.Scope != g.Scope || current.AgentID != g.AgentID || current.TargetBootID != g.TargetBootID || current.ActionID != g.ActionID || current.CommandID != g.CommandID || current.Operation != g.Operation || current.Digest != g.Digest {
			return persistence.ErrConflict
		}
		return nil
	}}
	if err := persistence.NewExecutionGrantRepository(storage).Authorize(r.Context(), g); err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, map[string]bool{"authorized": true})
}
func (s *Server) handleInspectExecutionGrant(w http.ResponseWriter, r *http.Request) {
	if err := s.authenticatedGrantAgent(r); err != nil {
		writeError(w, 401, err)
		return
	}
	var scope controlplane.ExecutionScope
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&scope); err != nil {
		writeError(w, 400, err)
		return
	}
	g, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore}).InspectGrant(r.Context(), r.PathValue("agent"), scope)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, g)
}

func (s *Server) handleRevokeExecutionGrant(w http.ResponseWriter, r *http.Request) {
	if err := s.authenticatedGrantAgent(r); err != nil {
		writeError(w, 401, err)
		return
	}
	var g controlplane.ExecutionGrant
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&g); err != nil || g.AgentID != r.PathValue("agent") {
		writeError(w, 400, persistence.ErrInvalidRecord)
		return
	}
	if err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore}).Revoke(r.Context(), g); err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, map[string]bool{"revoked": true})
}

func (s *Server) handleStoppedExecutionGrant(w http.ResponseWriter, r *http.Request) {
	var receipt controlplane.SignedExecutionStopReceipt
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768)).Decode(&receipt); err != nil {
		writeError(w, 400, err)
		return
	}
	key := s.grantHostKeys[r.PathValue("agent")]
	if receipt.Grant.AgentID != r.PathValue("agent") || controlplane.VerifyExecutionStopReceipt(key, receipt) != nil {
		writeError(w, 401, controlplane.ErrInvalidExecutionGrant)
		return
	}
	if receipt.Grant.Operation == "adoption_apply" {
		// Adoption must durably commit evidence and operation completion before
		// releasing this scope. The dedicated completion route owns that order.
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	sum := sha256.Sum256(raw)
	repo := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	// Commit quiescence atomically. Exact receipt replay remains successful
	// even after another grant has been issued for the same scope.
	if receipt.Grant.Operation == "guest_file_install" {
		if err = repo.ValidateHostStop(r.Context(), receipt.Grant, hex.EncodeToString(sum[:])); err == nil {
			_, err = s.apiStore.CompleteFileFromHost(r.Context(), receipt.Grant.AgentID, receipt.InstallID, receipt)
		}
		if err != nil {
			writeError(w, agentRepositoryErrorStatus(err), err)
			return
		}
	}
	err = repo.AcceptHostStop(r.Context(), receipt.Grant, hex.EncodeToString(sum[:]))
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, map[string]bool{"stopped": true})
}

type HostNodeOperationCompletion struct {
	Receipt  controlplane.SignedExecutionStopReceipt `json:"receipt"`
	Evidence json.RawMessage                         `json:"evidence"`
}

func (s *Server) handleHostNodeOperationCompletion(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	var body HostNodeOperationCompletion
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768)).Decode(&body); err != nil {
		writeError(w, 400, err)
		return
	}
	key := s.grantHostKeys[agentID]
	if body.Receipt.Grant.AgentID != agentID || controlplane.VerifyAdoptionStopReceipt(key, body.Receipt, body.Evidence) != nil {
		writeError(w, 409, controlplane.ErrInvalidExecutionGrant)
		return
	}
	op, err := s.apiStore.GetNodeOperation(r.Context(), r.PathValue("operation"))
	if err != nil || op.ID != body.Receipt.Grant.ActionID {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	if op.Adoption == nil || len(body.Evidence) == 0 {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	digest, err := op.Adoption.Digest()
	g := body.Receipt.Grant
	if err != nil || digest != g.Digest || op.CommandID != g.CommandID || op.AgentID != agentID || op.Adoption.AgentID != agentID || op.Adoption.TopologyUID != g.Scope.TopologyUID || op.ResourceUID != g.Scope.ResourceUID || op.Generation != g.Scope.Generation {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	// A trusted signature is not proof this epoch was issued. Validate the
	// durable authority before any canonical mutation, without releasing it.
	rawReceipt, err := json.Marshal(body.Receipt)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	receiptHash := sha256.Sum256(rawReceipt)
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	if err := authority.ValidateHostStop(r.Context(), body.Receipt.Grant, hex.EncodeToString(receiptHash[:])); err != nil {
		writeError(w, 409, err)
		return
	}
	if err := s.commitAdoptionState(r.Context(), *op, body.Evidence); err != nil {
		writeError(w, 409, err)
		return
	}
	completed, err := s.apiStore.CompleteAdoptionFromHost(r.Context(), agentID, op.ID, body.Receipt)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	// A failure here keeps the old scope blocked. An identical host receipt
	// retries the journal and operation idempotently, then finishes retirement.
	if err := authority.AcceptHostStop(r.Context(), body.Receipt.Grant, hex.EncodeToString(receiptHash[:])); err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 200, completed)
}

func (s *Server) commitAdoptionState(ctx context.Context, op controlplane.NodeOperation, evidence json.RawMessage) error {
	if op.Adoption == nil {
		return persistence.ErrConflict
	}
	req := op.Adoption
	addr, err := address.Parse(req.Address)
	if err != nil {
		return err
	}
	repo, err := s.actionRepository(req.Topology)
	if err != nil {
		return err
	}
	// The server owns this cached repository across requests. Closing it here
	// breaks receipt retries and subsequent lifecycle operations on SQL stores.
	resource := state.Resource{Address: addr, ResourceType: addr.Type, Driver: req.Provider, ExternalID: req.ExternalID, OwnerUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: req.Generation, Private: append(json.RawMessage(nil), evidence...), Status: state.ResourcePresent, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	return runtime.CommitAdoptionEvidence(ctx, repo, op.ID, req.Topology, addr, req.Evidence, resource, substrate.OperationIdentity{ActionID: op.ID, OwnerUID: req.TopologyUID, TopologyUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: req.Generation}, req.ExpectedSerial)
}
func (s *Server) authenticatedGrantAgent(r *http.Request) error {
	id := r.PathValue("agent")
	a, err := s.apiStore.GetAgent(r.Context(), id)
	if err != nil {
		return err
	}
	// Unprotected development agents cannot acquire signed effect credentials.
	if a.AuthSecret == "" || a.SecretHash == "" || a.IsBlocked() {
		return persistence.ErrConflict
	}
	return s.verifyAgentRequest(r, id)
}
func (s *Server) handleExecutionSession(w http.ResponseWriter, r *http.Request) {
	if err := s.authenticatedGrantAgent(r); err != nil {
		writeError(w, 401, err)
		return
	}
	var request struct {
		IncarnationID string `json:"incarnation_id"`
		Expected      string `json:"expected_session"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&request); err != nil {
		writeError(w, 400, err)
		return
	}
	repo := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	session, err := repo.OpenSession(r.Context(), r.PathValue("agent"), request.IncarnationID, request.Expected)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, session)
}

type ExecutionGrantRequest struct {
	SessionID  string `json:"session_id"`
	LeaseOwner string `json:"lease_owner"`
	Attempt    int    `json:"attempt"`
	BootID     string `json:"boot_id"`
}

type ExecutionGrantAuthorization struct {
	Grant      controlplane.SignedExecutionGrant `json:"grant"`
	LeaseOwner string                            `json:"lease_owner"`
}

func (s *Server) handleAuthorizeExecutionGrant(w http.ResponseWriter, r *http.Request) {
	if err := s.authenticatedGrantAgent(r); err != nil {
		writeError(w, 401, err)
		return
	}
	if len(s.grantSigner) != ed25519.PrivateKeySize {
		writeError(w, 503, errors.New("execution grant issuer key not configured"))
		return
	}
	var request ExecutionGrantAuthorization
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	if err := d.Decode(&request); err != nil {
		writeError(w, 400, err)
		return
	}
	// A temporal rejection is itself an observation of the authority clock.
	// Persist it before Verify can return, otherwise a later wall-clock step
	// could make the same previously expired signed envelope appear live.
	if err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore}).ObserveTime(r.Context()); err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if err := controlplane.VerifyExecutionGrant(s.grantSigner.Public().(ed25519.PublicKey), request.Grant, request.Grant.BootID, time.Now()); err != nil {
		writeError(w, 409, err)
		return
	}
	g := request.Grant.Grant
	renew := strings.HasSuffix(r.URL.Path, "/renew")
	commitCommand := true
	var leaseDeadline time.Time
	proof := ExecutionGrantRequest{SessionID: g.SessionID, LeaseOwner: request.LeaseOwner, Attempt: int(g.Attempt), BootID: request.Grant.BootID}
	storage := apiExecutionGrantStorage{store: s.apiStore, commandID: r.PathValue("command"), writeCommand: renew, commitCommand: &commitCommand, validate: func(record *persistence.CommandRecord) error {
		current, err := s.sourceGrant(r.Context(), r.PathValue("agent"), proof, record)
		if err != nil {
			return err
		}
		if current.Scope != g.Scope || current.ActionID != g.ActionID || current.CommandID != g.CommandID || current.Operation != g.Operation || current.Digest != g.Digest {
			return persistence.ErrConflict
		}
		if renew {
			until := time.Now().UTC().Add(controlplane.ExecutionGrantMaxTTL)
			if until.After(record.Command.LeaseUntil) {
				record.Command.LeaseUntil = until
			}
			leaseDeadline = record.Command.LeaseUntil
		}
		return nil
	}}
	if renew {
		var granted *controlplane.ExecutionGrant
		var transitionErr error
		err := storage.UpdateExecutionAuthority(r.Context(), func(raw []byte) ([]byte, error) {
			memory := &grantTransaction{raw: raw}
			repo := persistence.NewExecutionGrantRepository(memory)
			granted, transitionErr = repo.Renew(r.Context(), g, controlplane.ExecutionGrantMaxTTL/2)
			if transitionErr != nil {
				commitCommand = false
				return memory.raw, nil
			}
			if granted.ExpiresAt.After(leaseDeadline) {
				commitCommand = false
				transitionErr = persistence.ErrConflict
				memory.raw = raw
				if err := repo.ObserveTime(r.Context()); err != nil {
					return nil, err
				}
				return memory.raw, nil
			}
			return memory.raw, nil
		})
		if err == nil {
			err = transitionErr
		}
		if err != nil {
			writeError(w, agentRepositoryErrorStatus(err), err)
			return
		}
		signed, err := controlplane.SignExecutionGrant(s.grantSigner, *granted, request.Grant.BootID, time.Now().UTC())
		if err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, 200, signed)
		return
	}
	if err := persistence.NewExecutionGrantRepository(storage).Authorize(r.Context(), g); err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, map[string]bool{"authorized": true})
}

// sourceGrant derives scope and payload exclusively from durable dispatch data.
// The caller cannot ask the API to sign another resource, action, or digest.
func (s *Server) sourceGrant(ctx context.Context, agentID string, proof ExecutionGrantRequest, record *persistence.CommandRecord) (controlplane.ExecutionGrant, error) {
	if record == nil || record.Retired {
		return controlplane.ExecutionGrant{}, persistence.ErrConflict
	}
	cmd := record.Command
	if cmd.AgentID != agentID || cmd.Attempt != proof.Attempt || cmd.Attempt < 1 || cmd.LeaseOwner == "" || cmd.LeaseOwner != proof.LeaseOwner || !cmd.LeaseUntil.After(time.Now()) || cmd.IsTerminal() {
		return controlplane.ExecutionGrant{}, persistence.ErrConflict
	}
	operation := cmd.Type
	digest := ""
	topologyRun := false
	switch cmd.Type {
	case "node_operation":
		if cmd.Operation.Operation == "adoption_apply" {
			request, err := controlplane.FromAdoptionCommand(cmd)
			if err != nil {
				return controlplane.ExecutionGrant{}, err
			}
			digest, err = request.Digest()
			if err != nil {
				return controlplane.ExecutionGrant{}, err
			}
			operation = "adoption_apply"
			break
		}
		if cmd.Operation.Operation == "ssh_access_apply" || cmd.Operation.Operation == "ssh_access_delete" {
			request, err := controlplane.FromSSHAccessCommand(cmd)
			if err != nil {
				return controlplane.ExecutionGrant{}, err
			}
			digest, err = request.Digest()
			if err != nil {
				return controlplane.ExecutionGrant{}, err
			}
			operation = cmd.Operation.Operation
			break
		}
		request, err := controlplane.FromNodeOperationCommand(cmd)
		if err != nil {
			return controlplane.ExecutionGrant{}, err
		}
		digest, err = request.Digest()
		if err != nil {
			return controlplane.ExecutionGrant{}, err
		}
		operation = "node_power"
	case "guest_execution":
		if cmd.Execution == nil {
			return controlplane.ExecutionGrant{}, persistence.ErrCorruptRecord
		}
		task, err := guesttask.FromExecutionCommand(cmd)
		if err != nil {
			return controlplane.ExecutionGrant{}, err
		}
		digest, err = task.Digest()
		if err != nil {
			return controlplane.ExecutionGrant{}, err
		}
		operation = "guest_task"
	case "guest_file_put":
		request, err := controlplane.FromFileInstallCommand(cmd)
		if err != nil {
			return controlplane.ExecutionGrant{}, err
		}
		digest, err = request.Digest()
		if err != nil {
			return controlplane.ExecutionGrant{}, err
		}
		operation = "guest_file_install"
	case "run_assigned":
		action := ""
		if cmd.Run != nil {
			action = cmd.Run.Op
			if action == "" {
				action = cmd.Run.Operation
			}
		}
		if cmd.Run == nil || (action != "apply" && action != "destroy") || cmd.Run.Topology == "" || cmd.Run.Revision == "" {
			return controlplane.ExecutionGrant{}, persistence.ErrCorruptRecord
		}
		req := hostsupervisor.RunRequest{RunID: cmd.Run.ID, Topology: cmd.Run.Topology, TopologyUID: cmd.TopologyUID, Revision: cmd.Run.Revision, ActionID: cmd.ActionID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation, Action: action, Target: cmd.Run.Target, ExpectedSerial: cmd.ExpectedSerial}
		var err error
		digest, err = req.Digest()
		if err != nil {
			return controlplane.ExecutionGrant{}, err
		}
		operation = "run_" + action
		topologyRun = cmd.ResourceUID == ""
	default:
		return controlplane.ExecutionGrant{}, persistence.ErrUnsupportedVersion
	}
	if cmd.TopologyUID == "" || cmd.ActionID == "" || (!topologyRun && (cmd.ResourceUID == "" || cmd.Generation < 1)) || (topologyRun && cmd.Generation != 0) {
		return controlplane.ExecutionGrant{}, persistence.ErrConflict
	}
	return controlplane.ExecutionGrant{TargetBootID: proof.BootID, Scope: controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation}, AgentID: agentID, SessionID: proof.SessionID, ActionID: cmd.ActionID, CommandID: cmd.ID, Attempt: uint64(cmd.Attempt), Operation: operation, Digest: digest, ExpiresAt: cmd.LeaseUntil}, nil
}

func (s *Server) handleExecutionGrant(w http.ResponseWriter, r *http.Request) {
	if err := s.authenticatedGrantAgent(r); err != nil {
		writeError(w, 401, err)
		return
	}
	if len(s.grantSigner) != ed25519.PrivateKeySize {
		writeError(w, 503, errors.New("execution grant issuer key not configured"))
		return
	}
	var proof ExecutionGrantRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proof); err != nil || proof.SessionID == "" || proof.BootID == "" {
		writeError(w, 400, persistence.ErrInvalidRecord)
		return
	}
	var request controlplane.ExecutionGrant
	storage := apiExecutionGrantStorage{store: s.apiStore, commandID: r.PathValue("command"), validate: func(record *persistence.CommandRecord) error {
		var err error
		request, err = s.sourceGrant(r.Context(), r.PathValue("agent"), proof, record)
		return err
	}}
	// The callback below executes only after sourceGrant ran under the command
	// lock. It uses the exact lease deadline rather than a worker-selected TTL.
	var granted *controlplane.ExecutionGrant
	var transitionErr error
	err := storage.UpdateExecutionAuthority(r.Context(), func(raw []byte) ([]byte, error) {
		memory := &grantTransaction{raw: raw}
		repo := persistence.NewExecutionGrantRepository(memory)
		ttl := time.Until(request.ExpiresAt)
		if ttl > persistence.MaxExecutionGrantTTL {
			ttl = persistence.MaxExecutionGrantTTL
		}
		granted, transitionErr = repo.Issue(r.Context(), request, ttl)
		return memory.raw, nil
	})
	if err == nil {
		err = transitionErr
	}
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	signed, err := controlplane.SignExecutionGrant(s.grantSigner, *granted, proof.BootID, time.Now().UTC())
	if err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 200, signed)
}

// grantTransaction lets the shared state machine operate on bytes already
// protected by the API's compound authority/command transaction.
type grantTransaction struct{ raw []byte }

func (t *grantTransaction) ReadExecutionAuthority(context.Context) ([]byte, error) {
	if t.raw == nil {
		return nil, persistence.ErrNotFound
	}
	return append([]byte(nil), t.raw...), nil
}

func (t *grantTransaction) UpdateExecutionAuthority(_ context.Context, fn func([]byte) ([]byte, error)) error {
	raw, err := fn(t.raw)
	if err == nil {
		t.raw = raw
	}
	return err
}
