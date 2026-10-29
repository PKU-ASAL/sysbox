package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
)

type historicalIntent struct {
	RunID               string               `json:"run_id"`
	OwnerUID            string               `json:"owner_uid"`
	ResourceUID         string               `json:"resource_uid"`
	Generation          int64                `json:"generation"`
	JournalRevision     int64                `json:"journal_revision"`
	JournalSHA256       string               `json:"journal_sha256"`
	Blockers            []string             `json:"blockers"`
	ID                  string               `json:"id"`
	Topology            string               `json:"topology"`
	Resource            string               `json:"resource"`
	Action              string               `json:"action"`
	Status              runtime.ActionStatus `json:"status"`
	ProviderOperationID string               `json:"provider_operation_id,omitempty"`
	Quiescence          string               `json:"quiescence,omitempty"`
	ExternalID          string               `json:"external_id,omitempty"`
	Resolution          string               `json:"resolution"`
	Reason              string               `json:"reason"`
}

func (s *Server) handleHistoricalIntents(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := controlstore.ValidatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	repo, err := s.actionRepository(topology)
	if err != nil {
		writeError(w, stateOperationErrorStatus(err), err)
		return
	}
	catalog, err := repo.ListTopologies(r.Context())
	if err != nil {
		writeError(w, stateOperationErrorStatus(err), err)
		return
	}
	found := false
	for _, entry := range catalog {
		if entry.Name == topology {
			found = true
		}
	}
	if !found {
		writeJSON(w, http.StatusOK, map[string]any{"topology": topology, "intents": []historicalIntent{}, "read_only": true})
		return
	}
	_, actions, err := repo.LoadStateAndActions(r.Context(), topology)
	if err != nil {
		writeError(w, stateOperationErrorStatus(err), err)
		return
	}
	out := make([]historicalIntent, 0)
	for _, a := range actions {
		if a.Status == runtime.ActionSucceeded {
			continue
		}
		item := historicalIntent{ID: a.ID, Topology: a.Topology, Resource: a.Resource.String(), Action: string(a.Action), Status: a.Status, ProviderOperationID: a.ProviderOperationID, Quiescence: a.Quiescence, Resolution: "manual_reconciliation_required", Reason: "historical intent has no independently verified provider completion"}
		if a.Candidate != nil {
			item.ExternalID = a.Candidate.ExternalID
		}
		item.RunID = a.RunID
		item.OwnerUID = a.OwnerUID
		item.ResourceUID = a.ResourceUID
		item.Generation = a.Generation
		item.JournalRevision = a.Revision
		raw, err := json.Marshal(a)
		if err != nil {
			writeError(w, 503, err)
			return
		}
		item.JournalSHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
		item.Blockers = []string{}
		if a.ProviderOperationID == "" || a.Quiescence == "" {
			item.Blockers = append(item.Blockers, "missing_original_operation_anchor")
		}
		if a.Quiescence == "unknown" {
			item.Blockers = append(item.Blockers, "provider_inflight_stop_unproven")
		}
		if a.Action == model.PlanActionReset && !a.EffectComplete {
			item.Blockers = append(item.Blockers, "explicit_reset_phase_continuation_required")
		}
		if a.Action != model.PlanActionDelete && a.ReplayForbidden && !a.EffectComplete {
			item.Blockers = append(item.Blockers, "guest_effect_completion_unproven")
		}
		if a.Candidate == nil {
			item.Blockers = append(item.Blockers, "missing_external_identity_receipt")
		}
		if len(item.Blockers) == 0 {
			item.Resolution = "observation_recovery_required"
			item.Reason = "durable return evidence exists; owning-host observation and original authorization still required"
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, http.StatusOK, map[string]any{"topology": topology, "intents": out, "read_only": true})
}
