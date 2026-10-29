package api

import (
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"sort"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) handleGuestTaskStops(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agent")
	owner, err := s.apiStore.GetAgent(r.Context(), id)
	// Blocked agents may drain their old effects, but development/unsigned
	// identities cannot obtain credentials even for cancellation.
	if err != nil || owner == nil || owner.AuthSecret == "" || owner.SecretHash == "" || s.verifyAgentRequest(r, id) != nil {
		writeError(w, 401, persistence.ErrConflict)
		return
	}
	if len(s.grantSigner) != ed25519.PrivateKeySize {
		writeError(w, 503, controlplane.ErrInvalidExecutionGrant)
		return
	}
	var cursor controlplane.GuestTaskStopCursor
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF || (cursor.ThroughEpoch != 0 && cursor.AfterEpoch > cursor.ThroughEpoch) {
		writeError(w, 400, persistence.ErrInvalidRecord)
		return
	}
	grants, err := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore}).ListActiveGrants(r.Context())
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	ceiling := cursor.ThroughEpoch
	if ceiling == 0 {
		for _, g := range grants {
			if g.Epoch > ceiling {
				ceiling = g.Epoch
			}
		}
	}
	candidates := make([]controlplane.ExecutionGrant, 0)
	parents := make(map[uint64]controlplane.ExecutionGrant)
	for _, g := range grants {
		if g.AgentID == id && g.ParentEpoch == 0 && controlplane.IsRunExecutionOperation(g.Operation) {
			parents[g.Epoch] = g
		}
		if g.AgentID == id && g.State == controlplane.GrantRevoked && g.Operation == "guest_task" && g.Epoch > cursor.AfterEpoch && g.Epoch <= ceiling {
			candidates = append(candidates, g)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Epoch < candidates[j].Epoch })
	page := controlplane.GuestTaskStopPage{Stops: []controlplane.GuestTaskStop{}, ThroughEpoch: ceiling}
	if len(candidates) > controlplane.GuestTaskStopPageSize {
		candidates = candidates[:controlplane.GuestTaskStopPageSize]
		page.NextEpoch = candidates[len(candidates)-1].Epoch
	}
	for _, g := range candidates {
		if err := r.Context().Err(); err != nil {
			writeError(w, 500, err)
			return
		}
		if g.ParentEpoch != 0 {
			stop, err := s.runTaskStop(r.Context(), g, parents[g.ParentEpoch])
			if err != nil {
				page.Issues = append(page.Issues, controlplane.GuestTaskStopIssue{Epoch: g.Epoch, ActionID: g.ActionID})
			} else {
				page.Stops = append(page.Stops, stop)
			}
			continue
		}
		command, err := readFailureCommand(r.Context(), s.apiStore, g.CommandID)
		if err == nil && command != nil && command.Command.Type == "session_open" {
			stop, err := s.consoleTaskStop(r.Context(), command.Command, g)
			if err != nil {
				page.Issues = append(page.Issues, controlplane.GuestTaskStopIssue{Epoch: g.Epoch, ActionID: g.ActionID})
			} else {
				page.Stops = append(page.Stops, stop)
			}
			continue
		}
		x, err := s.apiStore.GetGuestExecution(r.Context(), g.ActionID)
		if err != nil || x.AgentID != id || x.CommandID != g.CommandID || controlstore.ValidatePathSegment(x.Topology, "topology") != nil || controlstore.ValidatePathSegment(x.Node, "node") != nil {
			page.Issues = append(page.Issues, controlplane.GuestTaskStopIssue{Epoch: g.Epoch, ActionID: g.ActionID})
			continue
		}
		// A committed result is retried from its host receipt, not regenerated from
		// a new stop request (private request/output may already be redacted).
		if guestExecutionTerminal(x.Status) {
			continue
		}
		command, err = readFailureCommand(r.Context(), s.apiStore, g.CommandID)
		if err != nil || command == nil || command.Command.AgentID != id || command.Command.Type != "guest_execution" || command.Command.TopologyUID != g.Scope.TopologyUID || command.Command.ResourceUID != g.Scope.ResourceUID || command.Command.Generation != g.Scope.Generation {
			page.Issues = append(page.Issues, controlplane.GuestTaskStopIssue{Epoch: g.Epoch, ActionID: g.ActionID})
			continue
		}
		task, err := guesttask.FromExecutionCommand(controlplane.AgentCommand{Type: "guest_execution", ActionID: x.ID, Execution: x, ExecutionRequest: x.Request})
		if err != nil {
			page.Issues = append(page.Issues, controlplane.GuestTaskStopIssue{Epoch: g.Epoch, ActionID: g.ActionID})
			continue
		}
		digest, err := task.Digest()
		if err != nil || digest != g.Digest || command.Command.ActionID != x.ID {
			page.Issues = append(page.Issues, controlplane.GuestTaskStopIssue{Epoch: g.Epoch, ActionID: g.ActionID})
			continue
		}
		proof, err := controlplane.SignExecutionCancellation(s.grantSigner, g)
		if err != nil {
			page.Issues = append(page.Issues, controlplane.GuestTaskStopIssue{Epoch: g.Epoch, ActionID: g.ActionID})
			continue
		}
		page.Stops = append(page.Stops, controlplane.GuestTaskStop{Topology: x.Topology, Node: x.Node, Cancellation: proof})
	}
	writeJSON(w, 200, page)
}
