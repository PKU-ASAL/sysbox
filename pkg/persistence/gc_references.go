package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/address"
	"sort"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

// GCReferenceSnapshot is a coherent observation of one lifecycle aggregate.
// It excludes the separate API activity/authority registries and is therefore
// only one input to GC planning, never a deletion authorization.
type GCReferenceDomain struct {
	Status string `json:"status"`
	Digest string `json:"digest,omitempty"`
	Count  int    `json:"count"`
}
type GCReferenceSnapshot struct {
	TopologyUID string                       `json:"topology_uid"`
	Serial      int64                        `json:"serial"`
	Digest      string                       `json:"digest"`
	References  []GCResourceReference        `json:"references"`
	Domains     map[string]GCReferenceDomain `json:"domains"`
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
		var err error
		out, err = gcReferences(a)
		return err
	})
	if err != nil {
		return GCReferenceSnapshot{}, err
	}
	r.externalGCMu.RLock()
	provider := r.externalGCInventory
	r.externalGCMu.RUnlock()
	if provider != nil {
		external, err := provider(ctx, topology)
		if err != nil {
			return GCReferenceSnapshot{}, fmt.Errorf("external GC inventory: %w", err)
		}
		if err := mergeExternalDomains(&out, external); err != nil {
			return GCReferenceSnapshot{}, err
		}
		bound, _ := json.Marshal(out.Domains)
		sum := sha256.Sum256(append([]byte(out.Digest+"\x00"), bound...))
		out.Digest = fmt.Sprintf("%x", sum[:])
	}
	return out, nil
}

func mergeExternalDomains(out *GCReferenceSnapshot, external map[string][]string) error {
	allowed := map[string]bool{"revision": true, "artifact": true, "cache": true, "placement": true}
	if len(external) != len(allowed) {
		return fmt.Errorf("%w: external GC inventory must enumerate all four domains", ErrCorruptRecord)
	}
	for domain, values := range external {
		if !allowed[domain] {
			return fmt.Errorf("%w: invalid external GC domain %q", ErrCorruptRecord, domain)
		}
		clean := append([]string(nil), values...)
		for _, value := range clean {
			if value == "" {
				return fmt.Errorf("%w: empty external GC reference in %s", ErrCorruptRecord, domain)
			}
		}
		sort.Strings(clean)
		local := out.Domains[domain]
		raw, _ := json.Marshal(struct {
			Local    GCReferenceDomain `json:"local"`
			External []string          `json:"external"`
		}{local, clean})
		sum := sha256.Sum256(append([]byte("sysbox.gc-domains.v1\x00"), raw...))
		out.Domains[domain] = GCReferenceDomain{Status: "complete", Digest: fmt.Sprintf("%x", sum[:]), Count: local.Count + len(clean)}
	}
	return nil
}

func gcReferences(a *aggregate) (GCReferenceSnapshot, error) { return gcReferencesAt(a, time.Now()) }

func gcReferencesAt(a *aggregate, now time.Time) (GCReferenceSnapshot, error) {
	var out GCReferenceSnapshot
	err := func() error {
		out.TopologyUID, out.Serial = a.State.TopologyUID, a.Serial
		out.Domains = map[string]GCReferenceDomain{"revision": {Status: "not_applicable"}, "artifact": {Status: "not_applicable"}, "cache": {Status: "not_applicable"}, "placement": {Status: "not_applicable"}}
		domainValues := map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}
		var domainErr error
		appendDomain := func(resource state.Resource) {
			attrs := resource.Attributes
			for domain, key := range map[string]string{"revision": "revision", "artifact": "artifact_digest", "cache": "cache_key", "placement": "placement_agent"} {
				if value, ok := attrs[key]; ok {
					v, ok := value.(string)
					if !ok || v == "" {
						domainErr = ErrCorruptRecord
						continue
					}
					domainValues[domain] = append(domainValues[domain], v)
				}
			}
		}
		appendResource := func(source string, resource state.Resource) {
			appendDomain(resource)
			out.References = append(out.References, GCResourceReference{Source: source, ResourceType: resource.Address.Type, Driver: resource.Driver, ExternalID: resource.ExternalID, OwnerUID: resource.OwnerUID, ResourceUID: resource.ResourceUID, Generation: resource.Generation})
		}
		collect := func(prefix string, content *aggregate) error {
			if content.State.TopologyUID != a.State.TopologyUID {
				return ErrCorruptRecord
			}
			if err := collectGCSources(content, prefix == "", prefix, &out, domainValues, now); err != nil {
				return err
			}
			for _, resource := range content.State.Resources {
				appendResource(prefix+"state/"+resource.Address.String(), resource)
			}
			for id, record := range content.IntentDispositions {
				for actionID, old := range record.Actions {
					if old.Action == controlplane.PlanActionReset && len(old.ResetSSH) > 0 {
						if resolved, err := runtime.ResetSSHWithObservedResults(old, record.Observation.Resources); err == nil {
							old = resolved
						}
					}
					for _, dep := range old.ResetSSH {
						appendResource(prefix+"intent-ssh-previous/"+id+"/"+actionID+"/"+dep.ActionID, dep.Previous)
						if dep.Result != nil {
							appendResource(prefix+"intent-ssh-result/"+id+"/"+actionID+"/"+dep.ActionID, *dep.Result)
						}
					}
					if old.Candidate != nil {
						appendResource(prefix+"intent-history/"+id+"/"+actionID, *old.Candidate)
					}
					if old.Replacement != nil {
						appendResource(prefix+"intent-replacement/"+id+"/"+actionID, *old.Replacement)
					}
				}
			}
			for id, recovery := range content.AdoptionRecoveries {
				for actionID, old := range recovery.Actions {
					if old.Candidate != nil {
						appendResource(prefix+"adoption-history/"+id+"/"+actionID, *old.Candidate)
					}
				}
				proof := recovery.Proof
				if proof.Disposition() == "cancel-source" || proof.Disposition() == "cancel-unowned" {
					addr, err := address.Parse(proof.Adoption.Address)
					if err != nil {
						return err
					}
					o := proof.Observation
					appendResource(prefix+"adoption-cancel/"+id, state.Resource{Address: addr, Driver: proof.Adoption.Provider, ExternalID: o.ExternalID, OwnerUID: o.OwnerUID, ResourceUID: o.ResourceUID, Generation: o.Generation})
				}
			}
			for id, action := range content.Actions {
				for _, dep := range action.ResetSSH {
					appendResource(prefix+"action-ssh-previous/"+id+"/"+dep.ActionID, dep.Previous)
					if dep.Result != nil {
						appendResource(prefix+"action-ssh-result/"+id+"/"+dep.ActionID, *dep.Result)
					}
				}
				if !runtime.ActionSettled(action) {
					return fmt.Errorf("%w: unresolved action %s%s", ErrConflict, prefix, id)
				}
				if action.Candidate != nil {
					appendResource(prefix+"action/"+id, *action.Candidate)
				}
				if action.Replacement != nil {
					appendResource(prefix+"replacement/"+id, *action.Replacement)
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
		if domainErr != nil {
			return domainErr
		}
		for domain, values := range domainValues {
			if len(values) > 0 {
				sort.Strings(values)
				raw, _ := json.Marshal(values)
				sum := sha256.Sum256(raw)
				out.Domains[domain] = GCReferenceDomain{Status: "complete", Digest: fmt.Sprintf("%x", sum[:]), Count: len(values)}
			}
		}
		sort.Slice(out.References, func(i, j int) bool { return out.References[i].Source < out.References[j].Source })
		// Bind all journal revisions and retained snapshots, even those without
		// resource handles. A reference-only hash would miss pending plan changes.
		content := *a
		content.GC = map[string]GCReservation{}
		if content.Format == GCAdmissionFormatVersion {
			content.Format = content.GCReferenceFormat
		}
		content.GCReferenceFormat = 0
		raw, err := json.Marshal(&content)
		if err != nil {
			return err
		}
		out.Digest = fmt.Sprintf("%x", sha256.Sum256(append([]byte("sysbox.gc-lifecycle.v1\x00"), raw...)))
		return nil
	}()
	if err != nil {
		return GCReferenceSnapshot{}, err
	}
	return out, nil
}
