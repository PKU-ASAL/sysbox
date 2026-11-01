package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) handleHostObservation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agent")
	if err := s.verifyAgentRequest(r, id); err != nil {
		writeError(w, 401, err)
		return
	}
	var observation controlplane.HostObservation
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 9<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&observation); err != nil {
		writeError(w, 400, err)
		return
	}
	if observation.AgentID != id || controlplane.VerifyHostObservation(s.grantHostKeys[id], observation) != nil || !controlplane.FreshHostObservation(observation.ObservedAt, time.Now()) {
		writeError(w, 401, controlplane.ErrInvalidExecutionGrant)
		return
	}
	inv := observation.Inventory
	if inv.AgentID != id || inv.SessionID != observation.SessionID || inv.Sequence != observation.Sequence || !controlplane.FreshHostObservation(inv.ObservedAt, time.Now()) {
		writeError(w, 400, persistence.ErrInvalidRecord)
		return
	}
	raw, err := persistence.EncodeInventory(inv)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	records := []persistence.ExecutionObservation{{AgentID: id, SessionID: inv.SessionID, Kind: "inventory", Sequence: inv.Sequence, Data: raw}}
	for _, p := range observation.Projections {
		if p.AgentID != id || p.SessionID != observation.SessionID || p.Sequence != observation.Sequence || !controlplane.FreshHostObservation(p.ObservedAt, time.Now()) || controlstore.ValidatePathSegment(p.Topology, "topology") != nil {
			writeError(w, 400, persistence.ErrInvalidRecord)
			return
		}
		raw, err := persistence.EncodeProjection(p)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		records = append(records, persistence.ExecutionObservation{AgentID: id, SessionID: p.SessionID, Kind: "projection/" + p.Topology, Sequence: p.Sequence, Data: raw})
	}
	if err = persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore}).PublishObservations(r.Context(), records); err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	for _, p := range observation.Projections {
		s.agents.SaveResourceProjection(p)
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}
