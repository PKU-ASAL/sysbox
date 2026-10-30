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
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
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
	s.mux.HandleFunc("POST /v1/agents/{agent}/guest-task-stops", s.handleGuestTaskStops)
	s.mux.HandleFunc("POST /v1/agents/{agent}/execution-session", s.handleExecutionSession)
	s.mux.HandleFunc("POST /v1/agents/{agent}/commands/{command}/execution-grant", s.handleExecutionGrant)
	s.mux.HandleFunc("POST /v1/agents/{agent}/commands/{command}/execution-grant/authorize", s.handleAuthorizeExecutionGrant)
	s.mux.HandleFunc("POST /v1/agents/{agent}/commands/{command}/execution-grant/renew", s.handleAuthorizeExecutionGrant)
	s.mux.HandleFunc("POST /v1/agents/{agent}/execution-grant/derive-guest", s.handleDeriveGuestExecutionGrant)
	s.mux.HandleFunc("POST /v1/agents/{agent}/execution-grant/derive-ssh", s.handleDeriveSSHExecutionGrant)
	s.mux.HandleFunc("POST /v1/hosts/{agent}/derived-ssh/{operation}/completed", s.handleDerivedSSHCompletion)
	s.mux.HandleFunc("POST /v1/agents/{agent}/execution-grant/renew-guest", s.handleRenewDerivedGuestExecutionGrant)
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
	// Compatibility endpoint only proves an existing server-issued delivery.
	// Workers cannot synthesize leases or overwrite the reserved outbox seed.
	record, err := readFailureCommand(r.Context(), s.apiStore, request.CommandID)
	if err != nil || record == nil || record.Retired {
		writeError(w, http.StatusConflict, persistence.ErrConflict)
		return
	}
	cmd := record.Command
	topology, err := controlstore.GrantCommandTopology(record)
	candidate := controlplane.GCCandidate{Kind: cmd.GCKind, ExternalID: cmd.GCExternalID, TopologyUID: cmd.TopologyUID, OwnerUID: cmd.TopologyUID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation, InventoryDigest: cmd.GCInventoryDigest}
	if err != nil || topology != request.Topology || cmd.Type != "gc_delete" || cmd.AgentID != r.PathValue("agent") || cmd.ActionID != cmd.ID || cmd.ActionID != request.ActionID || candidate != request.Candidate {
		writeError(w, http.StatusConflict, persistence.ErrConflict)
		return
	}
	issued, err := s.issueCommandGrant(r.Context(), r.PathValue("agent"), request.CommandID, ExecutionGrantRequest{SessionID: request.SessionID, LeaseOwner: request.LeaseOwner, Attempt: request.Attempt, BootID: request.BootID})
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

func (s *Server) handleDeriveGuestExecutionGrant(w http.ResponseWriter, r *http.Request) {
	if err := s.authenticatedGrantAgent(r); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if len(s.grantSigner) != ed25519.PrivateKeySize {
		writeError(w, http.StatusServiceUnavailable, controlplane.ErrInvalidExecutionGrant)
		return
	}
	var request controlplane.GuestGrantDerivation
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.TaskID == "" || request.TaskDigest == "" || request.ResourceUID == "" || request.Generation < 1 || request.GuestBootID == "" {
		writeError(w, http.StatusBadRequest, persistence.ErrInvalidRecord)
		return
	}
	parent := request.Parent
	if err := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore}).ObserveTime(r.Context()); err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if controlplane.VerifyGuestGrantDerivation(s.grantHostKeys[r.PathValue("agent")], request) != nil {
		writeError(w, http.StatusUnauthorized, controlplane.ErrInvalidExecutionGrant)
		return
	}
	if parent.Grant.AgentID != r.PathValue("agent") || controlplane.VerifyExecutionGrant(s.grantSigner.Public().(ed25519.PublicKey), parent, parent.BootID, time.Now()) != nil {
		writeError(w, http.StatusConflict, controlplane.ErrInvalidExecutionGrant)
		return
	}
	child := controlplane.ExecutionGrant{TargetBootID: request.GuestBootID, Scope: controlplane.ExecutionScope{TopologyUID: parent.Grant.Scope.TopologyUID, ResourceUID: request.ResourceUID, Generation: request.Generation}, AgentID: parent.Grant.AgentID, SessionID: parent.Grant.SessionID, ActionID: request.TaskID, CommandID: parent.Grant.CommandID, Attempt: parent.Grant.Attempt, Operation: "guest_task", Digest: request.TaskDigest, ExpiresAt: parent.Grant.ExpiresAt}
	ttl := time.Until(parent.Grant.ExpiresAt)
	derived, err := mustDeriveGuestGrant(r.Context(), s.derivedGuestStorage(r.Context(), parent), parent.Grant, child, ttl)
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

func mustDeriveGuestGrant(ctx context.Context, storage controlstore.AuthorityStorage, parent, child controlplane.ExecutionGrant, ttl time.Duration) (*controlplane.ExecutionGrant, error) {
	var result *controlplane.ExecutionGrant
	var transitionErr error
	err := storage.UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
		memory := &controlstore.GrantTransaction{Raw: raw}
		result, transitionErr = persistence.NewExecutionGrantRepository(memory).DeriveGuestTask(ctx, parent, child, ttl)
		return memory.Raw, nil
	})
	if err == nil {
		err = transitionErr
	}
	return result, err
}

type hostRunCompletion struct {
	Receipt  controlplane.SignedExecutionStopReceipt `json:"receipt"`
	Evidence json.RawMessage                         `json:"evidence"`
}

// handleHostRunCompletion is the host-owned completion boundary for remote
// apply/destroy. The worker cannot manufacture a terminal Run: the signed host
// receipt, durable command identity and immutable Run incarnation must agree.
func (s *Server) handleHostRunCompletion(w http.ResponseWriter, r *http.Request) {
	agentID, runID := r.PathValue("agent"), r.PathValue("run")
	if err := controlstore.ValidatePathSegment(agentID, "agent"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := controlstore.ValidatePathSegment(runID, "run"); err != nil {
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
	if grant.AgentID != agentID || grant.ActionID == "" || controlplane.VerifyRunStopReceipt(key, body.Receipt, body.Evidence) != nil {
		writeError(w, http.StatusConflict, controlplane.ErrInvalidExecutionGrant)
		return
	}
	rawReceipt, err := json.Marshal(body.Receipt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	receiptHash := sha256.Sum256(rawReceipt)
	authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore})
	if err := authority.PrepareHostStop(r.Context(), body.Receipt.Grant, hex.EncodeToString(receiptHash[:])); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	var observed controlplane.Run
	if err := json.Unmarshal(body.Evidence, &observed); err != nil || observed.ID != runID || !observed.Status.IsTerminal() {
		writeError(w, http.StatusConflict, persistence.ErrConflict)
		return
	}
	completed, err := s.apiStore.CompleteRunFromHost(r.Context(), agentID, runID, body.Receipt, body.Evidence)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if err := authority.AcceptHostStop(r.Context(), body.Receipt.Grant, hex.EncodeToString(receiptHash[:])); err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	// Canonical host evidence, not a final worker event, completes execution.
	// Retire delivery only after both result commit and authority release; a
	// lost reply retries these idempotent steps while retaining the command seed.
	if err := s.apiStore.DeleteAgentCommand(r.Context(), agentID, grant.CommandID); err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if s.jobs != nil {
		s.jobs.remember(completed)
		s.jobs.logs.Close(completed.ID)
	}
	writeJSON(w, http.StatusOK, controlplane.HostRunResult{Run: *completed, Cleanup: completed.Cleanup})
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
	base := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore})
	if err := controlplane.VerifyExecutionGrant(s.grantSigner.Public().(ed25519.PublicKey), signed, signed.BootID, time.Now()); err != nil {
		// Rejections before the compound transaction must still advance the
		// durable clock floor so wall-clock rollback cannot revive a proof.
		if clockErr := base.ObserveTime(r.Context()); clockErr != nil {
			writeError(w, agentRepositoryErrorStatus(clockErr), clockErr)
			return
		}
		writeError(w, 409, err)
		return
	}
	g := signed.Grant
	if g.ParentEpoch != 0 {
		// Derived authorization has early parent/source rejection paths.
		if err := base.ObserveTime(r.Context()); err != nil {
			writeError(w, agentRepositoryErrorStatus(err), err)
			return
		}
		s.authorizeDerivedGuestGrant(w, r, g)
		return
	}
	storage := s.commandGrantStorage(g.CommandID, func(record *persistence.CommandRecord) error {
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
	})
	// Authorize commits its clock observation with source and grant checks.
	if err := persistence.NewExecutionGrantRepository(storage).Authorize(r.Context(), g); err != nil {
		// A missing command or storage failure may precede the transaction's
		// clock update. Preserve the previous rejection-time guarantee.
		if clockErr := observeRejectedAuthorization(r.Context(), base); clockErr != nil {
			writeError(w, agentRepositoryErrorStatus(clockErr), clockErr)
			return
		}
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, map[string]bool{"authorized": true})
}

// A canceled validation may have rolled back its clock observation. This
// bounded cleanup only advances the clock; it never grants execution rights.
func observeRejectedAuthorization(ctx context.Context, repo *persistence.ExecutionGrantRepository) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	return repo.ObserveTime(cleanup)
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
	g, err := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore}).InspectGrant(r.Context(), r.PathValue("agent"), scope)
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
	if err := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore}).Revoke(r.Context(), g); err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, map[string]bool{"revoked": true})
}

func (s *Server) handleStoppedExecutionGrant(w http.ResponseWriter, r *http.Request) {
	var receipt controlplane.SignedExecutionStopReceipt
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&receipt); err != nil {
		writeError(w, 400, err)
		return
	}
	key := s.grantHostKeys[r.PathValue("agent")]
	if receipt.Grant.AgentID != r.PathValue("agent") || controlplane.VerifyExecutionStopReceipt(key, receipt) != nil {
		writeError(w, 401, controlplane.ErrInvalidExecutionGrant)
		return
	}
	if receipt.Grant.Operation == "node_power" {
		if err := s.completePowerFromHost(r.Context(), receipt); err != nil {
			writeError(w, agentRepositoryErrorStatus(err), err)
			return
		}
		writeJSON(w, 200, map[string]bool{"stopped": true, "completed": true})
		return
	}
	if receipt.Grant.Operation == "gc_delete" {
		if err := s.completeGCFromHost(r.Context(), receipt, key); err != nil {
			writeError(w, agentRepositoryErrorStatus(err), err)
			return
		}
		writeJSON(w, 200, map[string]bool{"stopped": true, "completed": true})
		return
	}
	if receipt.Grant.Operation == "adoption_inspect" || receipt.Grant.Operation == "adoption_apply" || controlplane.IsRunExecutionOperation(receipt.Grant.Operation) || receipt.Grant.Operation == "ssh_access_apply" || receipt.Grant.Operation == "ssh_access_delete" || controlplane.IsGuestSSHAccessOperation(receipt.Grant.Operation) {
		// Adoption, Run and SSH completion must durably commit their result
		// before releasing this scope. Dedicated completion routes own that order.
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	sum := sha256.Sum256(raw)
	repo := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore})
	if receipt.Grant.Operation == "guest_task" && receipt.Grant.ParentEpoch == 0 {
		command, readErr := readFailureCommand(r.Context(), s.apiStore, receipt.Grant.CommandID)
		if readErr != nil {
			writeError(w, agentRepositoryErrorStatus(readErr), readErr)
			return
		}
		if command != nil && command.Command.Type == "session_open" {
			if err = s.completeConsoleFromHost(r.Context(), command.Command, receipt); err != nil {
				writeError(w, agentRepositoryErrorStatus(err), err)
				return
			}
			writeJSON(w, 200, map[string]bool{"stopped": true, "completed": true})
			return
		}
		if receipt.TaskID == "" || receipt.TaskID != receipt.Grant.ActionID || receipt.Grant.AgentID != r.PathValue("agent") {
			err = persistence.ErrConflict
		} else {
			err = repo.ValidateHostStop(r.Context(), receipt.Grant, hex.EncodeToString(sum[:]))
			if err != nil {
				writeError(w, agentRepositoryErrorStatus(err), err)
				return
			}
			_, err = s.apiStore.CompleteGuestExecutionFromHost(r.Context(), receipt.Grant.AgentID, receipt.TaskID, receipt)
			if err == nil {
				err = repo.AcceptHostStop(r.Context(), receipt.Grant, hex.EncodeToString(sum[:]))
			}
		}
		if err != nil {
			writeError(w, agentRepositoryErrorStatus(err), err)
			return
		}
		writeJSON(w, 200, map[string]bool{"stopped": true, "completed": true})
		return
	}
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
	if body.Receipt.Grant.Operation == "adoption_inspect" {
		s.completeHostInspection(w, r, body)
		return
	}
	if body.Receipt.Grant.Operation == "ssh_access_apply" || body.Receipt.Grant.Operation == "ssh_access_delete" {
		s.completeHostSSHAccess(w, r, body)
		return
	}
	if controlplane.IsGuestSSHAccessOperation(body.Receipt.Grant.Operation) {
		s.completeHostGuestSSHAccess(w, r, body)
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
	authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore})
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
	if op.Adoption.Transfer != nil {
		barrier, err := authority.InspectMaintenance(r.Context(), op.Adoption.Transfer.Plan.ID)
		if err != nil {
			writeError(w, 409, err)
			return
		}
		if err = authority.EndMaintenance(r.Context(), *barrier); err != nil {
			writeError(w, 409, err)
			return
		}
	}
	writeJSON(w, 200, completed)
}

func (s *Server) commitAdoptionState(ctx context.Context, op controlplane.NodeOperation, evidence json.RawMessage) error {
	if op.Adoption == nil {
		return persistence.ErrConflict
	}
	var key ed25519.PublicKey
	if len(s.grantSigner) == ed25519.PrivateKeySize {
		key = s.grantSigner.Public().(ed25519.PublicKey)
	}
	return s.commitAdoptionWithAuthority(ctx, op.Adoption.Topology, func(raw, authority []byte) ([]byte, error) {
		return persistence.ApplyHostAdoptionState(raw, authority, op, evidence, key)
	})
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
	repo := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore})
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
	if err := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore}).ObserveTime(r.Context()); err != nil {
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
	storage := s.commandGrantStorage(r.PathValue("command"), func(record *persistence.CommandRecord) error {
		current, err := s.sourceGrant(r.Context(), r.PathValue("agent"), proof, record)
		if err != nil {
			return err
		}
		if current.Scope != g.Scope || current.ActionID != g.ActionID || current.CommandID != g.CommandID || current.Operation != g.Operation || current.Digest != g.Digest {
			return persistence.ErrConflict
		}
		if renew {
			until := time.Now().UTC().Add(controlplane.ExecutionGrantMaxTTL)
			if record.Command.Type == "session_open" {
				deadline := record.Command.Session.CreatedAt.Add(time.Duration(record.Command.Request.TimeoutSeconds) * time.Second)
				if until.After(deadline) {
					until = deadline
				}
			}
			if until.After(record.Command.LeaseUntil) {
				record.Command.LeaseUntil = until
			}
			leaseDeadline = record.Command.LeaseUntil
			if record.Command.Type == "session_open" {
				deadline := record.Command.Session.CreatedAt.Add(time.Duration(record.Command.Request.TimeoutSeconds) * time.Second)
				if leaseDeadline.After(deadline) {
					leaseDeadline = deadline
				}
			}
		}
		return nil
	})
	storage.WriteCommand, storage.CommitCommand = renew, &commitCommand
	if renew {
		var granted *controlplane.ExecutionGrant
		var transitionErr error
		err := storage.UpdateExecutionAuthority(r.Context(), func(raw []byte) ([]byte, error) {
			memory := &controlstore.GrantTransaction{Raw: raw}
			repo := persistence.NewExecutionGrantRepository(memory)
			granted, transitionErr = repo.RenewUntil(r.Context(), g, controlplane.ExecutionGrantMaxTTL/2, leaseDeadline)
			if transitionErr != nil {
				commitCommand = false
				return memory.Raw, nil
			}
			if granted.ExpiresAt.After(leaseDeadline) {
				commitCommand = false
				transitionErr = persistence.ErrConflict
				memory.Raw = raw
				if err := repo.ObserveTime(r.Context()); err != nil {
					return nil, err
				}
				return memory.Raw, nil
			}
			return memory.Raw, nil
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
	lifecycleRun := false
	switch cmd.Type {
	case "session_open":
		task, err := guesttask.FromConsoleCommand(cmd)
		if err != nil {
			return controlplane.ExecutionGrant{}, err
		}
		digest, err = task.Digest()
		if err != nil {
			return controlplane.ExecutionGrant{}, err
		}
		operation = "guest_task"
		deadline := cmd.Session.CreatedAt.Add(time.Duration(cmd.Request.TimeoutSeconds) * time.Second)
		if cmd.Request.TimeoutSeconds < 1 || !deadline.After(time.Now()) {
			return controlplane.ExecutionGrant{}, persistence.ErrConflict
		}
		if cmd.LeaseUntil.After(deadline) {
			cmd.LeaseUntil = deadline
		}
	case "node_operation":
		if cmd.Operation.Operation == "adoption_inspect" {
			request, err := controlplane.FromAdoptionInspectionCommand(cmd)
			if err != nil {
				return controlplane.ExecutionGrant{}, err
			}
			digest, err = request.Digest()
			if err != nil {
				return controlplane.ExecutionGrant{}, err
			}
			operation = "adoption_inspect"
			break
		}
		if controlplane.IsGuestSSHAccessOperation(cmd.Operation.Operation) {
			request, err := controlplane.FromGuestSSHAccessCommand(cmd)
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
		req, err := canonicalRunGrantRequest(cmd)
		if err != nil {
			return controlplane.ExecutionGrant{}, err
		}
		digest, err = req.Digest()
		if err != nil {
			return controlplane.ExecutionGrant{}, err
		}
		operation = "run_" + req.Action
		lifecycleRun = true
	case "gc_delete":
		if cmd.Session == nil || (cmd.Session.ID != "" && cmd.Session.ID != proof.SessionID) || cmd.GCKind == "" || cmd.GCExternalID == "" || cmd.GCInventoryDigest == "" {
			return controlplane.ExecutionGrant{}, persistence.ErrCorruptRecord
		}
		candidate := hostsupervisor.GCCandidate{Kind: cmd.GCKind, ExternalID: cmd.GCExternalID, TopologyUID: cmd.TopologyUID, OwnerUID: cmd.TopologyUID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation, InventoryDigest: cmd.GCInventoryDigest}
		var err error
		digest, err = candidate.Digest()
		if err != nil {
			return controlplane.ExecutionGrant{}, err
		}
	default:
		return controlplane.ExecutionGrant{}, persistence.ErrUnsupportedVersion
	}
	if cmd.TopologyUID == "" || cmd.ActionID == "" || (!lifecycleRun && (cmd.ResourceUID == "" || cmd.Generation < 1)) {
		return controlplane.ExecutionGrant{}, persistence.ErrConflict
	}
	scope := controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation}
	if lifecycleRun {
		// Runtime commits the whole topology aggregate. Target identity stays
		// bound by the request digest; children independently own node scope.
		scope.ResourceUID, scope.Generation = "", 0
	}
	return controlplane.ExecutionGrant{TargetBootID: proof.BootID, Scope: scope, AgentID: agentID, SessionID: proof.SessionID, ActionID: cmd.ActionID, CommandID: cmd.ID, Attempt: uint64(cmd.Attempt), Operation: operation, Digest: digest, ExpiresAt: cmd.LeaseUntil}, nil
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
	granted, err := s.issueCommandGrant(r.Context(), r.PathValue("agent"), r.PathValue("command"), proof)
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
