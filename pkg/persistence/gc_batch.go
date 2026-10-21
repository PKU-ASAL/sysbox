package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/oslab/sysbox/pkg/controlplane"
)

const MaxGCBatch = 128

// IDs bind topology, caller key and the entire normalized candidate list.
// No extra aggregate fields are introduced. Older readers reject simultaneous
// pending reservations rather than silently running a batch as independent work.
func NewGCBatch(topology, key, agent string, candidates []controlplane.GCCandidate) ([]GCReservation, error) {
	if !topologyName.MatchString(topology) || key == "" || len(key) > 128 || agent == "" || len(candidates) < 1 || len(candidates) > MaxGCBatch {
		return nil, ErrInvalidRecord
	}
	list := append([]controlplane.GCCandidate(nil), candidates...)
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if (a.Kind == "network") != (b.Kind == "network") {
			return b.Kind == "network"
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.ExternalID < b.ExternalID
	})
	seen := map[string]bool{}
	uids := map[string]bool{}
	for _, c := range list {
		if c.Validate() != nil || c.TopologyUID != list[0].TopologyUID || c.InventoryDigest != list[0].InventoryDigest {
			return nil, ErrInvalidRecord
		}
		id := c.Kind + "/" + c.ExternalID
		if seen[id] || uids[c.ResourceUID] {
			return nil, ErrInvalidRecord
		}
		seen[id] = true
		uids[c.ResourceUID] = true
	}
	raw, err := json.Marshal(struct {
		Agent      string
		Candidates []controlplane.GCCandidate
	}{agent, list})
	if err != nil {
		return nil, err
	}
	group := fmt.Sprintf("gcb-%x-%x", sha256.Sum256([]byte(topology+"\x00"+key)), sha256.Sum256(raw))
	out := make([]GCReservation, len(list))
	for i, c := range list {
		out[i] = GCReservation{ID: fmt.Sprintf("%s-%03d", group, i), AgentID: agent, Candidate: c}
	}
	return out, nil
}
func batchPrefix(id string) string {
	parts := strings.Split(id, "-")
	if len(parts) == 4 && parts[0] == "gcb" && len(parts[1]) == 64 && len(parts[2]) == 64 && len(parts[3]) == 3 {
		return strings.Join(parts[:3], "-")
	}
	return ""
}
func nextGC(a *aggregate) string {
	next := ""
	for id, r := range a.GC {
		if r.Pending() && (next == "" || id < next) {
			next = id
		}
	}
	return next
}

func ApplyGCReservations(raw, authority []byte, topology string, requests []GCReservation) ([]byte, error) {
	if len(requests) == 1 && batchPrefix(requests[0].ID) == "" {
		return ApplyGCReservation(raw, authority, topology, requests[0])
	}
	if len(requests) < 1 || len(requests) > MaxGCBatch {
		return nil, ErrInvalidRecord
	}
	a, err := decodeAggregate(raw)
	if err != nil {
		return nil, err
	}
	if err := validateAggregate(a, topology); err != nil {
		return nil, err
	}
	group := batchPrefix(requests[0].ID)
	if group == "" {
		return nil, ErrInvalidRecord
	}
	candidates := make([]controlplane.GCCandidate, len(requests))
	for i, r := range requests {
		candidates[i] = r.Candidate
	}
	validated, err := NewGCBatch(topology, "validate", requests[0].AgentID, candidates)
	if err != nil {
		return nil, err
	}
	if strings.Split(validated[0].ID, "-")[2] != strings.Split(requests[0].ID, "-")[2] {
		return nil, ErrConflict
	}
	for i, r := range requests {
		if r.Candidate != validated[i].Candidate {
			return nil, ErrConflict
		}
	}
	seen := map[string]bool{}
	for i, r := range requests {
		if r.ID != fmt.Sprintf("%s-%03d", group, i) || !r.Pending() || r.AgentID != requests[0].AgentID || r.Candidate.Validate() != nil || r.Candidate.TopologyUID != a.State.TopologyUID {
			return nil, ErrInvalidRecord
		}
		seen[r.ID] = true
	}
	// Replays cannot omit members or change any immutable field, even after all
	// commands have completed. A reused key with another payload is a conflict.
	keyPrefix := strings.Join(strings.Split(group, "-")[:2], "-") + "-"
	found := 0
	for id, old := range a.GC {
		if strings.HasPrefix(id, keyPrefix) {
			if !seen[id] {
				return nil, ErrConflict
			}
			for _, r := range requests {
				if r.ID == id && (r.AgentID != old.AgentID || r.Candidate != old.Candidate) {
					return nil, ErrConflict
				}
			}
			found++
		}
	}
	if found > 0 {
		if found != len(requests) {
			return nil, ErrConflict
		}
		return append([]byte(nil), raw...), nil
	}
	if gcPending(a) {
		return nil, ErrConflict
	}
	// First transition checks stopped authority and all references. Remaining
	// members are validated against the same pre-reservation snapshot, atomically.
	if _, err := ApplyGCReservation(raw, authority, topology, requests[0]); err != nil {
		return nil, err
	}
	for _, r := range requests {
		c := r.Candidate
		cmd := controlplane.AgentCommand{Type: "gc_delete", TopologyUID: c.TopologyUID, ResourceUID: c.ResourceUID, Generation: c.Generation, GCKind: c.Kind, GCExternalID: c.ExternalID, GCInventoryDigest: c.InventoryDigest}
		if err := validateGCReferences(a, cmd); err != nil {
			return nil, err
		}
		a.GC[r.ID] = r
	}
	if err := validateAggregate(a, topology); err != nil {
		return nil, err
	}
	return json.Marshal(a)
}
func (r *Repository) ReserveGCBatchWithAuthority(ctx context.Context, topology string, requests []GCReservation, authority []byte) error {
	return r.updateGC(ctx, topology, true, func(a *aggregate) error {
		raw, err := json.Marshal(a)
		if err != nil {
			return err
		}
		next, err := ApplyGCReservations(raw, authority, topology, requests)
		if err != nil {
			return err
		}
		decoded, err := decodeAggregate(next)
		if err != nil {
			return err
		}
		*a = *decoded
		return nil
	})
}
