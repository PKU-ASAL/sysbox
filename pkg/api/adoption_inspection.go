package api

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) handleAdoptionInspection(w http.ResponseWriter, r *http.Request) {
	topology, key := r.PathValue("topology"), r.Header.Get("Idempotency-Key")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, 400, err)
		return
	}
	if key == "" || len(key) > 128 {
		writeError(w, 400, fmt.Errorf("Idempotency-Key required"))
		return
	}
	var input ImportNodeRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, 400, err)
		return
	}
	addr, err := address.Parse(input.Type + "." + input.Name)
	if err != nil || addr.Type != "sysbox_node" || input.ID == "" || (input.Substrate != "docker" && input.Substrate != "libvirt" && input.Substrate != "firecracker") {
		writeError(w, 400, fmt.Errorf("node identity and supported provider required"))
		return
	}
	unlock := s.jobs.lockTopology(topology)
	defer unlock()
	id := fmt.Sprintf("inspect-%x", sha256.Sum256([]byte(topology+"\x00"+key)))
	old, readErr := s.apiStore.GetNodeOperation(r.Context(), id)
	if readErr != nil && !errors.Is(readErr, persistence.ErrNotFound) {
		writeError(w, 503, readErr)
		return
	}
	if old != nil {
		if old.Inspection == nil || old.Inspection.Address != addr.String() || old.Inspection.ExternalID != input.ID || old.Inspection.Provider != input.Substrate {
			writeError(w, 409, persistence.ErrConflict)
			return
		}
		writeJSON(w, 202, old)
		return
	}
	st, err := s.workspaceService().LoadState(topology)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	if st.FindResource(addr) != nil {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	owner, err := s.nodeOperations().scheduler.SelectAgentForTopology(r.Context(), topology, []string{input.Substrate}, "")
	if err != nil {
		writeError(w, 409, err)
		return
	}
	resourceUID := fmt.Sprintf("%x", sha256.Sum256([]byte(st.TopologyUID+"\x00"+addr.String()+"\x00"+id)))
	inspection := controlplane.AdoptionInspectionRequest{ID: id, Topology: topology, TopologyUID: st.TopologyUID, Address: addr.String(), Provider: input.Substrate, AgentID: owner.ID, ExternalID: input.ID, ResourceUID: resourceUID, Generation: 1, ExpectedSerial: st.Meta.Serial}
	subject := s.requestSubject(r)
	op := prepareNodeOperation(controlplane.NodeOperation{ID: id, Topology: topology, Workspace: topology, Operation: "adoption_inspect", Node: addr.Name, Name: addr.Name, Type: addr.Type, Substrate: input.Substrate, ExternalID: input.ID, AgentID: owner.ID, ResourceUID: resourceUID, Generation: 1, Inspection: &inspection, RequestedBy: subject.User, Roles: subject.Roles})
	op, err = s.nodeOperations().enqueue(r.Context(), st.TopologyUID, op)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 202, op)
}

// Confirmation consumes persisted host evidence by ID, not a caller-supplied
// provider payload. The provider will compare the same fingerprint at mutation.
func (s *Server) handleConfirmAdoptionInspection(w http.ResponseWriter, r *http.Request) {
	topology, id, key := r.PathValue("topology"), r.PathValue("inspection"), r.Header.Get("Idempotency-Key")
	if validatePathSegment(topology, "topology") != nil || validatePathSegment(id, "inspection") != nil || key == "" || len(key) > 128 {
		writeError(w, 400, fmt.Errorf("valid topology, inspection and Idempotency-Key required"))
		return
	}
	inspected, err := s.apiStore.GetNodeOperation(r.Context(), id)
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	if inspected == nil || inspected.Operation != "adoption_inspect" || inspected.Topology != topology || inspected.Status != controlplane.NodeOperationStatusDone || inspected.Inspection == nil || inspected.InspectionEvidence == nil {
		writeError(w, 409, persistence.ErrConflict)
		return
	}
	source := inspected.Inspection
	evidence := *inspected.InspectionEvidence
	requestID := fmt.Sprintf("adopt-%x", sha256.Sum256([]byte(topology+"\x00"+key)))
	resourceUID, generation := source.ResourceUID, source.Generation
	if managed := evidence.ManagedIdentity; managed != nil {
		if managed.Validate() != nil || managed.OwnerUID != source.TopologyUID || managed.TopologyUID != source.TopologyUID {
			writeError(w, 409, fmt.Errorf("managed resource belongs to another topology"))
			return
		}
		resourceUID, generation = managed.ResourceUID, managed.Generation
	}
	request := controlplane.AdoptionRequest{ID: requestID, Topology: topology, TopologyUID: source.TopologyUID, Address: source.Address, Provider: source.Provider, AgentID: source.AgentID, ExternalID: evidence.ExternalID, ResourceUID: resourceUID, Generation: generation, ExpectedSerial: source.ExpectedSerial, Evidence: evidence}
	prior, readErr := s.apiStore.GetNodeOperation(r.Context(), requestID)
	if readErr != nil && !errors.Is(readErr, persistence.ErrNotFound) {
		writeError(w, 503, readErr)
		return
	}
	if prior != nil {
		expected, _ := request.Digest()
		actual := ""
		if prior.Adoption != nil {
			actual, _ = prior.Adoption.Digest()
		}
		if expected == "" || expected != actual {
			writeError(w, 409, persistence.ErrConflict)
			return
		}
		writeJSON(w, 202, prior)
		return
	}
	op, err := s.nodeOperations().AdoptionConfirm(r.Context(), request, s.requestSubject(r))
	if err != nil {
		writeError(w, agentRepositoryErrorStatus(err), err)
		return
	}
	writeJSON(w, 202, op)
}
