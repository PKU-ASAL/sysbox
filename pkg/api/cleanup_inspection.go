package api

import (
	"errors"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
)

// GET inspection reports durable evidence only. Lists from the operation store
// and lifecycle snapshot are not one distributed transaction; neither this
// response nor an empty list authorizes metadata deletion or proves absence.
func (s *Server) handleCleanupInspection(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, 400, err)
		return
	}
	unavailable := func() { writeError(w, http.StatusServiceUnavailable, errors.New("cleanup evidence unavailable")) }
	repo, err := s.actionRepository(topology)
	if err != nil {
		unavailable()
		return
	}
	snapshot, err := repo.InspectLifecycle(r.Context(), topology)
	if err != nil {
		unavailable()
		return
	}
	runs, err := s.apiStore.LoadRuns(r.Context())
	if err != nil {
		unavailable()
		return
	}
	nodes, err := s.apiStore.ListNodeOperations(r.Context(), "")
	if err != nil {
		unavailable()
		return
	}
	placement, err := s.workspaceService().loadPlacement(topology)
	if err != nil {
		unavailable()
		return
	}
	_, err = os.Stat(s.workspaceService().HCLFile(topology))
	metadataExists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		unavailable()
		return
	}
	resources := make([]map[string]any, 0, len(snapshot.State.Resources))
	for _, res := range snapshot.State.Resources {
		resources = append(resources, map[string]any{"resource": res.Address.String(), "name": res.Address.Name, "type": res.ResourceType, "provider": res.Driver, "external_id": res.ExternalID, "status": res.Status, "owner_uid": res.OwnerUID, "resource_uid": res.ResourceUID, "generation": res.Generation, "recorded_at": res.UpdatedAt})
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i]["resource"].(string) < resources[j]["resource"].(string) })
	actions := []map[string]any{}
	for _, a := range snapshot.Actions {
		if a.Status == runtime.ActionSucceeded {
			continue
		}
		actions = append(actions, map[string]any{"id": a.ID, "run_id": a.RunID, "resource": a.Resource.String(), "action": a.Action, "status": a.Status, "owner_uid": a.OwnerUID, "resource_uid": a.ResourceUID, "generation": a.Generation, "revision": a.Revision})
	}
	active := []map[string]any{}
	agents := map[string]bool{}
	if placement != nil {
		agents[placement.AgentID] = true
	}
	for _, run := range latestRunsByID(runs) {
		if run.Topology != topology {
			continue
		}
		if run.AgentID != "" {
			agents[run.AgentID] = true
		}
		if run.Status.IsActive() {
			active = append(active, map[string]any{"kind": "run", "id": run.ID, "op": run.Op, "status": run.Status, "agent_id": run.AgentID, "queued_at": run.QueuedAt, "revision": run.Revision})
		}
	}
	for _, op := range nodes {
		if op.Topology != topology {
			continue
		}
		if op.AgentID != "" {
			agents[op.AgentID] = true
		}
		if op.Status == controlplane.NodeOperationStatusDone || op.Status == controlplane.NodeOperationStatusFailed {
			continue
		}
		active = append(active, map[string]any{"kind": "node_operation", "id": op.ID, "op": op.Operation, "status": op.Status, "agent_id": op.AgentID, "resource_uid": op.ResourceUID, "generation": op.Generation})
	}
	sort.Slice(active, func(i, j int) bool {
		return active[i]["kind"].(string)+active[i]["id"].(string) < active[j]["kind"].(string)+active[j]["id"].(string)
	})
	scope := "local_repository"
	agentIDs := []string{}
	for id := range agents {
		agentIDs = append(agentIDs, id)
	}
	sort.Strings(agentIDs)
	if len(agentIDs) > 0 {
		scope = "owning_agent_not_observed"
	}
	result := map[string]any{"resources": resources, "active_operations": active, "unfinished_actions": actions, "state_exists": snapshot.StateExists, "metadata_exists": metadataExists, "source": "lifecycle_repository", "scope": scope, "agent_ids": agentIDs, "external_presence": "unknown", "inspected_at": time.Now().UTC(), "recorded_at": snapshot.State.Meta.UpdatedAt, "state_serial": snapshot.State.Meta.Serial, "topology_uid": snapshot.State.TopologyUID, "consistency": "atomic_lifecycle_separate_operation_snapshots"}
	if placement != nil {
		result["agent_id"] = placement.AgentID
	}
	result["active_operation_kinds"] = []string{"run", "node_operation"}
	receipt, reason := currentCleanupReceipt(topology, placement, runs, nodes)
	result["cleanup_receipt_status"] = reason
	if receipt != nil {
		result["cleanup_receipt"] = receipt
		result["cleanup_receipt_source"] = "owning_agent_durable_destroy_completion"
		if len(actions) > 0 {
			result["cleanup_receipt_status"] = "blocked_by_local_journal"
		}
		if len(resources) > 0 {
			result["cleanup_receipt_status"] = "blocked_by_local_resources"
		}
	}
	writeJSON(w, http.StatusOK, result)
}
