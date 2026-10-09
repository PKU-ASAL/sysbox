package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

// GCReferenceSnapshot is a coherent observation of one lifecycle aggregate.
// It excludes the separate API activity/authority registries and is therefore
// only one input to GC planning, never a deletion authorization.
type GCReferenceSnapshot struct {
	TopologyUID string                `json:"topology_uid"`
	Serial      int64                 `json:"serial"`
	Digest      string                `json:"digest"`
	References  []GCResourceReference `json:"references"`
}

// GCResourceReference deliberately contains no provider private state or keys.
type GCResourceReference struct {
	Source       string `json:"source"`
	ResourceType string `json:"resource_type"`
	Driver       string `json:"driver"`
	ExternalID   string `json:"external_id"`
	OwnerUID     string `json:"owner_uid"`
	ResourceUID  string `json:"resource_uid"`
	Generation   int64  `json:"generation"`
}

// LoadGCReferences validates current and retained journals and observes all
// lifecycle references under one backend transaction. An unresolved action or
// invalid snapshot prevents publication of even a partial reference set.
func (r *Repository) LoadGCReferences(ctx context.Context, topology string) (GCReferenceSnapshot, error) {
	var out GCReferenceSnapshot
	err := r.update(ctx, topology, func(a *aggregate) error {
		out.TopologyUID, out.Serial = a.State.TopologyUID, a.Serial
		appendResource := func(source string, resource state.Resource) {
			out.References = append(out.References, GCResourceReference{Source: source, ResourceType: resource.Address.Type, Driver: resource.Driver, ExternalID: resource.ExternalID, OwnerUID: resource.OwnerUID, ResourceUID: resource.ResourceUID, Generation: resource.Generation})
		}
		collect := func(prefix string, content *aggregate) error {
			if content.State.TopologyUID != a.State.TopologyUID {
				return ErrCorruptRecord
			}
			for _, resource := range content.State.Resources {
				appendResource(prefix+"state/"+resource.Address.String(), resource)
			}
			for id, action := range content.Actions {
				if action.Status != runtime.ActionSucceeded {
					return fmt.Errorf("%w: unresolved action %s%s", ErrConflict, prefix, id)
				}
				if action.Candidate != nil {
					appendResource(prefix+"action/"+id, *action.Candidate)
				}
			}
			return nil
		}
		if err := collect("", a); err != nil {
			return err
		}
		for id, snapshot := range a.Snapshots {
			content, err := decodeSnapshot(id, snapshot)
			if err != nil {
				return err
			}
			if err := collect("snapshot/"+id+"/", content); err != nil {
				return err
			}
		}
		sort.Slice(out.References, func(i, j int) bool { return out.References[i].Source < out.References[j].Source })
		// Bind all journal revisions and retained snapshots, even those without
		// resource handles. A reference-only hash would miss pending plan changes.
		raw, err := json.Marshal(a)
		if err != nil {
			return err
		}
		out.Digest = fmt.Sprintf("%x", sha256.Sum256(append([]byte("sysbox.gc-lifecycle.v1\x00"), raw...)))
		return nil
	})
	if err != nil {
		return GCReferenceSnapshot{}, err
	}
	return out, nil
}
