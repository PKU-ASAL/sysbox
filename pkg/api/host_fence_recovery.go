package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
)

type hostFenceRecoveryAction struct {
	Disposition      runtime.IntentDispositionPlan `json:"disposition"`
	ID               string                        `json:"id"`
	Resource         string                        `json:"resource"`
	ResourceUID      string                        `json:"resource_uid"`
	Generation       int64                         `json:"generation"`
	Status           runtime.ActionStatus          `json:"status"`
	ObservedPresence string                        `json:"observed_presence"`
	Blockers         []string                      `json:"blockers"`
}
type hostFenceRecoveryPlan struct {
	Topology               string                    `json:"topology"`
	RunQueuedAt            time.Time                 `json:"run_queued_at"`
	RunRevision            string                    `json:"run_revision"`
	ExpectedSerial         *int64                    `json:"expected_serial,omitempty"`
	ObservationSHA256      string                    `json:"observation_sha256,omitempty"`
	ReconciliationStatus   string                    `json:"reconciliation_status"`
	ReleaseStatus          string                    `json:"release_status"`
	ObservationAttestation string                    `json:"observation_attestation"`
	FenceID                string                    `json:"fence_id"`
	RunID                  string                    `json:"run_id"`
	AgentID                string                    `json:"agent_id"`
	RequestSHA256          string                    `json:"request_sha256"`
	CurrentJournalSHA256   string                    `json:"current_journal_sha256,omitempty"`
	JournalMatches         bool                      `json:"journal_matches"`
	HostProcessExclusion   string                    `json:"host_process_exclusion"`
	ObservationStatus      string                    `json:"observation_status"`
	ObservationCommandID   string                    `json:"observation_command_id,omitempty"`
	ReadOnly               bool                      `json:"read_only"`
	ReleaseAllowed         bool                      `json:"release_allowed"`
	ReplayAllowed          bool                      `json:"replay_allowed"`
	Blockers               []string                  `json:"blockers"`
	Actions                []hostFenceRecoveryAction `json:"actions"`
}

// Preserve admission's original wire hash. The snapshot is diagnostic evidence;
// it never authorizes mutation or establishes cross-repository atomicity.
func hostFenceJournal(run controlplane.Run, serial int64, actions []runtime.ActionRecord) ([]runtime.ActionRecord, string, error) {
	return persistence.FenceJournal(run, serial, actions)
}

func (s *Server) fenceCommandStorage() (persistence.CommandStorage, error) {
	switch store := s.apiStore.(type) {
	case *localAPIStore:
		return localCommandStorage{store: store}, nil
	case *sqliteAPIStore:
		return sqliteCommandStorage{store: store}, nil
	case *postgresAPIStore:
		return postgresCommandStorage{store: store}, nil
	default:
		return nil, persistence.ErrUnsupportedVersion
	}
}

func (s *Server) fenceRecoveryPlan(ctx context.Context, fence persistence.HostFenceRecord, observation string) (hostFenceRecoveryPlan, *controlplane.Run, int64, error) {
	req := fence.Request
	digest, err := req.Digest()
	if err != nil {
		return hostFenceRecoveryPlan{}, nil, 0, err
	}
	plan := hostFenceRecoveryPlan{FenceID: req.ID, RunID: req.RunID, AgentID: req.Grant.AgentID, RequestSHA256: digest, ReadOnly: true, ObservationAttestation: "unverified", HostProcessExclusion: "unproven", ObservationStatus: "not_requested", Blockers: []string{"explicit_reconciliation_and_release_required"}, Actions: []hostFenceRecoveryAction{}}
	plan.Topology = req.Topology
	plan.ReconciliationStatus = "none"
	if rec := fence.Reconciliation; rec != nil {
		plan.ReconciliationStatus = "prepared"
		if rec.Completed {
			plan.ReconciliationStatus = "completed"
		}
	}
	plan.ReleaseStatus = "held"
	if rec := fence.IntentDisposition; rec != nil {
		plan.ReconciliationStatus = "prepared"
		if !rec.ReleasedAt.IsZero() {
			plan.RunQueuedAt = rec.Decision.RunQueuedAt
			plan.RunRevision = rec.Decision.RunRevision
			plan.ExpectedSerial = &rec.Decision.ExpectedSerial
			plan.ReconciliationStatus = "completed"
			plan.ReleaseStatus = "released"
			plan.HostProcessExclusion = "verified"
			plan.ObservationAttestation = "verified"
			plan.ObservationStatus = "reconciled"
			plan.ObservationSHA256 = rec.Decision.ObservationSHA256
			plan.ObservationCommandID = rec.Observation.Command.ID
			plan.Blockers = []string{}
			return plan, nil, rec.Decision.ExpectedSerial, nil
		}
	}

	if fence.Release != nil {
		plan.ReleaseStatus = "released"
		plan.Blockers = []string{}
	}
	if fence.Receipt == nil {
		plan.Blockers = append(plan.Blockers, "fence_receipt_missing")
	} else {
		plan.HostProcessExclusion = "verified"
	}
	run, err := s.apiStore.GetRun(ctx, req.RunID)
	if errors.Is(err, persistence.ErrNotFound) {
		plan.Blockers = append(plan.Blockers, "original_run_missing")
		return plan, nil, 0, nil
	}
	if err != nil {
		return plan, nil, 0, err
	}
	repo, err := s.actionRepository(req.Topology)
	if err != nil {
		return plan, nil, 0, err
	}
	// Listing first ensures diagnostics cannot initialize an absent lifecycle.
	catalog, err := repo.ListTopologies(ctx)
	if err != nil {
		return plan, nil, 0, err
	}
	found := false
	for _, entry := range catalog {
		if entry.Name == req.Topology {
			found = true
		}
	}
	if !found {
		plan.Blockers = append(plan.Blockers, "original_lifecycle_missing")
		return plan, run, 0, nil
	}
	st, actions, err := repo.LoadStateAndActions(ctx, req.Topology)
	if err != nil {
		return plan, nil, 0, err
	}
	plan.RunQueuedAt = run.QueuedAt
	plan.RunRevision = run.Revision
	plan.ExpectedSerial = &st.Meta.Serial
	selected, journal, err := hostFenceJournal(*run, st.Meta.Serial, actions)
	if err != nil {
		plan.Blockers = append(plan.Blockers, "original_journal_unavailable")
		return plan, run, st.Meta.Serial, nil
	}
	plan.CurrentJournalSHA256 = journal
	plan.JournalMatches = journal == req.JournalSHA256 && run.Topology == req.Topology && run.AgentID == req.Grant.AgentID && st.TopologyUID == req.Grant.Scope.TopologyUID
	if !plan.JournalMatches {
		plan.Blockers = append(plan.Blockers, "original_journal_changed")
	}
	for _, a := range selected {
		item := hostFenceRecoveryAction{Disposition: runtime.PlanIntentDisposition(a, nil), ID: a.ID, Resource: a.Resource.String(), ResourceUID: a.ResourceUID, Generation: a.Generation, Status: a.Status, ObservedPresence: "unknown", Blockers: []string{}}
		if a.Status != runtime.ActionSucceeded {
			if a.ProviderOperationID == "" || a.ProviderOperationAnchor == "" {
				item.Blockers = append(item.Blockers, "missing_original_operation_anchor")
			}
			if a.Quiescence == "unknown" || a.Quiescence == "" {
				item.Blockers = append(item.Blockers, "provider_operation_stop_unproven")
			}
			if a.Candidate == nil {
				item.Blockers = append(item.Blockers, "missing_external_identity_receipt")
			}
			if a.ReplayForbidden && !a.EffectComplete {
				item.Blockers = append(item.Blockers, "guest_effect_completion_unproven")
			}
			if a.Action == controlplane.PlanActionReset && !a.EffectComplete {
				item.Blockers = append(item.Blockers, "explicit_reset_phase_continuation_required")
			}
		}
		plan.Actions = append(plan.Actions, item)
	}
	if observation == "" {
		plan.Blockers = append(plan.Blockers, "owning_agent_observation_required")
		return plan, run, st.Meta.Serial, nil
	}
	if validatePathSegment(observation, "command") != nil {
		return plan, nil, 0, persistence.ErrInvalidRecord
	}
	storage, err := s.fenceCommandStorage()
	if err != nil {
		return plan, nil, 0, err
	}
	cmd, err := storage.ReadCommand(ctx, observation)
	if err != nil {
		return plan, nil, 0, err
	}
	if !fenceObservationBound(cmd, req) {
		return plan, nil, 0, persistence.ErrConflict
	}
	plan.ObservationCommandID = observation
	if !plan.JournalMatches || !fenceObservationMatches(cmd, req, *run, st.Meta.Serial) {
		plan.ObservationStatus = "stale"
		plan.Blockers = append(plan.Blockers, "observation_snapshot_changed")
		return plan, run, st.Meta.Serial, nil
	}
	report, err := s.apiStore.GetRunRecoveryReport(ctx, req.Grant.AgentID, observation)
	if errors.Is(err, persistence.ErrNotFound) {
		plan.ObservationStatus = "pending"
		plan.Blockers = append(plan.Blockers, "owning_agent_observation_pending")
		return plan, run, st.Meta.Serial, nil
	}
	if errors.Is(err, persistence.ErrConflict) {
		plan.ObservationStatus = "failed"
		plan.Blockers = append(plan.Blockers, "owning_agent_observation_failed")
		return plan, run, st.Meta.Serial, nil
	}
	if err != nil {
		return plan, nil, 0, err
	}
	if !fenceObservationReportMatches(fence, *run, st.Meta.Serial, selected, report) {
		plan.ObservationStatus = "mismatched"
		plan.Blockers = append(plan.Blockers, "observation_identity_or_coverage_mismatch")
		return plan, run, st.Meta.Serial, nil
	}
	plan.ObservationStatus = "matched"
	if report.HostObservation == nil {
		plan.Blockers = append(plan.Blockers, "observation_not_host_boot_attested")
	} else {
		binding := controlplane.FenceObservationBinding{RequestSHA256: digest, HostBootID: fence.Receipt.NewBootID}
		if err := controlplane.VerifyFenceObservation(s.grantHostKeys[req.Grant.AgentID], *report, binding, cmd.Command.LeaseOwner, cmd.Command.Attempt); err != nil {
			plan.ObservationAttestation = "invalid"
			plan.ObservationStatus = "mismatched"
			plan.Blockers = append(plan.Blockers, "host_observation_signature_invalid")
			return plan, run, st.Meta.Serial, nil
		}
		plan.ObservationAttestation = "verified"
		encoded, err := json.Marshal(report)
		if err != nil {
			return plan, nil, 0, err
		}
		plan.ObservationSHA256 = fmt.Sprintf("%x", sha256.Sum256(encoded))
	}
	byID := map[string]string{}
	for _, r := range report.Resources {
		byID[r.ActionID] = r.Status
	}
	for i := range plan.Actions {
		plan.Actions[i].ObservedPresence = byID[plan.Actions[i].ID]
		// Unverified observations remain diagnostic only and cannot improve a
		// disposition decision. The plan itself never authorizes release.
		if plan.ObservationAttestation == "verified" {
			plan.Actions[i].Disposition = runtime.PlanIntentDisposition(selected[i], report.Resources)
		}
	}
	return plan, run, st.Meta.Serial, nil
}
func fenceObservationBound(record *persistence.CommandRecord, req controlplane.HostFenceRequest) bool {
	if record == nil || record.Command.Run == nil {
		return false
	}
	c := record.Command
	return c.Type == controlplane.RunRecoveryCommand && c.RecoveryMode == controlplane.RunRecoveryObserve && c.AgentID == req.Grant.AgentID && c.ActionID == req.ID && c.TopologyUID == req.Grant.Scope.TopologyUID && c.ExpectedSerial >= 0 && c.Run.ID == req.RunID && c.Run.Topology == req.Topology && c.Run.AgentID == req.Grant.AgentID && uint64(c.Run.Attempt) == req.Grant.Attempt
}
func fenceObservationMatches(record *persistence.CommandRecord, req controlplane.HostFenceRequest, run controlplane.Run, serial int64) bool {
	if !fenceObservationBound(record, req) {
		return false
	}
	c := record.Command
	return c.ExpectedSerial == serial && persistence.SameRunIncarnation(*c.Run, run) && c.Run.Attempt == run.Attempt && c.Run.LeaseOwner == run.LeaseOwner
}
func fenceObservationReportMatches(fence persistence.HostFenceRecord, run controlplane.Run, serial int64, actions []runtime.ActionRecord, report *controlplane.RunRecoveryReport) bool {
	if fence.Receipt == nil || report == nil || report.Mode != controlplane.RunRecoveryObserve || report.RunID != run.ID || report.AgentID != fence.Request.Grant.AgentID || report.TopologyUID != fence.Request.Grant.Scope.TopologyUID || report.StateSerial != serial || !report.QueuedAt.Equal(run.QueuedAt) || report.Revision != run.Revision || report.RunAttempt != run.Attempt || report.RunLeaseOwner != run.LeaseOwner || report.ObservedAt.Before(fence.Receipt.ObservedAt) || report.ObservedAt.After(time.Now().UTC().Add(controlplane.ExecutionGrantMaxClockSkew)) || len(report.Resources) != len(actions) {
		return false
	}
	byID := map[string]runtime.ActionRecord{}
	for _, a := range actions {
		byID[a.ID] = a
	}
	seen := map[string]bool{}
	for _, r := range report.Resources {
		a, ok := byID[r.ActionID]
		if !ok || seen[r.ActionID] || r.Resource != a.Resource.String() || r.OwnerUID != a.OwnerUID || r.ResourceUID != a.ResourceUID || r.Generation != a.Generation || r.JournalStatus != string(a.Status) {
			return false
		}
		if a.Candidate != nil && r.ExternalID != a.Candidate.ExternalID {
			return false
		}
		if r.Status != "present" && r.Status != "absent" && r.Status != "unknown" {
			return false
		}
		seen[r.ActionID] = true
	}
	return true
}
func (s *Server) handleHostFenceRecoveryPlan(w http.ResponseWriter, r *http.Request) {
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, 403, err)
		return
	}
	fence, err := s.readHostFence(r)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	plan, _, _, err := s.fenceRecoveryPlan(r.Context(), fence, r.URL.Query().Get("observation"))
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 200, plan)
}
func (s *Server) handleHostFenceObserve(w http.ResponseWriter, r *http.Request) {
	if err := s.authorizeGuestOperation(s.requestSubject(r)); err != nil {
		writeError(w, 403, err)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 128 {
		writeError(w, 400, persistence.ErrInvalidRecord)
		return
	}
	fence, err := s.readHostFence(r)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	digest, err := fence.Request.Digest()
	if err != nil {
		writeError(w, 503, err)
		return
	}
	id := fmt.Sprintf("hfo-%x", sha256.Sum256([]byte(digest+"\x00"+key)))
	storage, err := s.fenceCommandStorage()
	if err != nil {
		writeError(w, 503, err)
		return
	}
	old, err := storage.ReadCommand(r.Context(), id)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if old != nil {
		if fence.Receipt == nil || !fenceObservationBound(old, fence.Request) {
			writeError(w, 409, persistence.ErrConflict)
			return
		}
		writeJSON(w, 202, map[string]string{"command_id": id, "run_id": fence.Request.RunID, "mode": controlplane.RunRecoveryObserve})
		return
	}
	plan, run, serial, err := s.fenceRecoveryPlan(r.Context(), fence, "")
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if fence.IntentDisposition != nil || fence.Reconciliation != nil || fence.Release != nil || fence.Receipt == nil || !plan.JournalMatches || run == nil {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	agent, err := s.apiStore.GetAgent(r.Context(), run.AgentID)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if agent.IsBlocked() || !slices.Contains(agent.Capabilities, controlplane.RunRecoveryCapability) {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	snapshot := persistence.DurableRun(*run)
	cmd := controlplane.AgentCommand{ID: id, AgentID: run.AgentID, Type: controlplane.RunRecoveryCommand, RecoveryMode: controlplane.RunRecoveryObserve, Run: &snapshot, TopologyUID: fence.Request.Grant.Scope.TopologyUID, ActionID: fence.Request.ID, ExpectedSerial: serial, Protocol: controlplane.AgentProtocolVersion, Status: controlplane.AgentCommandStatusQueued, CreatedAt: fence.Receipt.ObservedAt}
	created := false
	// Commit-once outbox: a concurrent retry must not rewrite a leased or
	// retired command back to queued after the optimistic read above.
	_, err = storage.UpdateCommand(r.Context(), id, func(current *persistence.CommandRecord) (*persistence.CommandRecord, error) {
		if current != nil {
			if !fenceObservationMatches(current, fence.Request, *run, serial) {
				return nil, persistence.ErrConflict
			}
			return nil, nil
		}
		created = true
		return &persistence.CommandRecord{Command: cmd}, nil
	})
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if created && s.agents != nil {
		_ = s.agents.PublishCommand(cmd.AgentID, cmd)
	}
	writeJSON(w, 202, map[string]string{"command_id": id, "run_id": run.ID, "mode": controlplane.RunRecoveryObserve})
}
