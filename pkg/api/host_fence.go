package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

type hostFenceTarget struct {
	LabID     string
	PublicKey ed25519.PublicKey
}

func (s *Server) loadHostFenceTargets() error {
	s.fenceTargets = map[string]hostFenceTarget{}
	for agent, target := range s.cfg.API.ExecutionGrant.DisposableFenceTargets {
		if validatePathSegment(agent, "agent") != nil || validatePathSegment(target.LabID, "lab") != nil || target.ControllerPublicKeyFile == "" {
			return fmt.Errorf("invalid disposable fence target")
		}
		key, err := os.ReadFile(target.ControllerPublicKeyFile)
		if err != nil {
			return err
		}
		if len(key) != ed25519.PublicKeySize {
			return fmt.Errorf("invalid fence controller public key")
		}
		for _, hostKey := range s.grantHostKeys {
			if bytes.Equal(key, hostKey) {
				return fmt.Errorf("fence controller must have an independent key")
			}
		}
		if len(s.grantSigner) == ed25519.PrivateKeySize && bytes.Equal(key, s.grantSigner.Public().(ed25519.PublicKey)) {
			return fmt.Errorf("fence controller must have an independent key")
		}
		s.fenceTargets[agent] = hostFenceTarget{LabID: target.LabID, PublicKey: append(ed25519.PublicKey(nil), key...)}
	}
	return nil
}
func decodeFenceBody(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("one JSON object required")
	}
	return nil
}
func (s *Server) handleBeginHostFence(w http.ResponseWriter, r *http.Request) {
	topology, key := r.PathValue("topology"), r.Header.Get("Idempotency-Key")
	if validatePathSegment(topology, "topology") != nil || key == "" || len(key) > 128 {
		writeError(w, 400, fmt.Errorf("valid topology and Idempotency-Key required"))
		return
	}
	var body struct {
		RunID string                      `json:"run_id"`
		Grant controlplane.ExecutionGrant `json:"grant"`
	}
	if err := decodeFenceBody(w, r, &body); err != nil {
		writeError(w, 400, err)
		return
	}
	id := fmt.Sprintf("hf-%x", sha256.Sum256([]byte(topology+"\x00"+key)))
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	prior, err := authority.ReadHostFence(r.Context(), id)
	if err == nil {
		a, _ := json.Marshal(prior.Request.Grant)
		b, _ := json.Marshal(body.Grant)
		if prior.Request.Topology != topology || prior.Request.RunID != body.RunID || !bytes.Equal(a, b) {
			writeError(w, 409, persistence.ErrConflict)
			return
		}
		writeJSON(w, 202, prior)
		return
	}
	if !errors.Is(err, persistence.ErrNotFound) {
		writeError(w, 503, err)
		return
	}
	target, ok := s.fenceTargets[body.Grant.AgentID]
	if !ok {
		writeError(w, 409, fmt.Errorf("Agent is not enrolled as a disposable fencing target"))
		return
	}
	run, err := s.apiStore.GetRun(r.Context(), body.RunID)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if run == nil || run.ID != body.Grant.ActionID || run.Topology != topology || run.AgentID != body.Grant.AgentID || uint64(run.Attempt) != body.Grant.Attempt || run.HostCompletion != nil || run.Status == controlplane.RunDone || body.Grant.Operation != "run_"+run.Op {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	repo, err := s.actionRepository(topology)
	if err != nil {
		writeError(w, 503, err)
		return
	}
	st, actions, err := repo.LoadStateAndActions(r.Context(), topology)
	if err != nil {
		writeError(w, 503, err)
		return
	}
	if st.TopologyUID != body.Grant.Scope.TopologyUID {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	_, journal, err := hostFenceJournal(*run, st.Meta.Serial, actions)
	if err != nil {
		writeError(w, 409, err)
		return
	}

	request := controlplane.HostFenceRequest{Version: 1, ID: id, Topology: topology, RunID: run.ID, LabID: target.LabID, ControllerKey: target.PublicKey, JournalSHA256: journal, Grant: body.Grant}
	// Validate the retained immutable dispatch under the same storage lock as
	// the freeze. The journal above remains an observed snapshot, not release authority.
	bound := s.commandGrantStorage(body.Grant.CommandID, func(record *persistence.CommandRecord) error {
		if record == nil {
			return persistence.ErrConflict
		}
		cmd := record.Command
		g := body.Grant
		if cmd.Run == nil || cmd.ID != g.CommandID || cmd.AgentID != g.AgentID || cmd.ActionID != g.ActionID || cmd.TopologyUID != g.Scope.TopologyUID || cmd.Attempt < 1 || uint64(cmd.Attempt) < g.Attempt || !persistence.SameRunIncarnation(*run, *cmd.Run) {
			return persistence.ErrConflict
		}
		payload, err := canonicalRunGrantRequest(cmd)
		if err != nil {
			return persistence.ErrConflict
		}
		digest, err := payload.Digest()
		if err != nil || digest != g.Digest || "run_"+payload.Action != g.Operation {
			return persistence.ErrConflict
		}
		return nil
	})
	accepted, err := persistence.NewExecutionGrantRepository(bound).BeginHostFence(r.Context(), request)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 202, accepted)
}
func (s *Server) handleGetHostFence(w http.ResponseWriter, r *http.Request) {
	record, err := s.readHostFence(r)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, record)
}
func (s *Server) readHostFence(r *http.Request) (persistence.HostFenceRecord, error) {
	topology, id := r.PathValue("topology"), r.PathValue("fence")
	if validatePathSegment(topology, "topology") != nil || validatePathSegment(id, "fence") != nil {
		return persistence.HostFenceRecord{}, persistence.ErrInvalidRecord
	}
	record, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore}).ReadHostFence(r.Context(), id)
	if err != nil {
		return record, err
	}
	if record.Request.Topology != topology {
		return persistence.HostFenceRecord{}, persistence.ErrNotFound
	}
	return record, nil
}
func (s *Server) handleRecordHostFence(w http.ResponseWriter, r *http.Request) {
	record, err := s.readHostFence(r)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	var receipt controlplane.HostFenceReceipt
	if err := decodeFenceBody(w, r, &receipt); err != nil {
		writeError(w, 400, err)
		return
	}
	repo := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	if err := repo.RecordHostFence(r.Context(), record.Request.ID, receipt); err != nil {
		writeError(w, 409, err)
		return
	}
	record, err = repo.ReadHostFence(r.Context(), record.Request.ID)
	if err != nil {
		writeError(w, 503, err)
		return
	}
	writeJSON(w, 200, record)
}
