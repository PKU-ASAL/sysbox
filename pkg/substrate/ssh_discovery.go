package substrate

import (
	"fmt"
	"strings"
)

// SSHAccessQuery is a read-only lookup within an expected node incarnation.
// It neither claims ownership nor authorizes a mutation.
type SSHAccessQuery struct {
	NodeID      string `json:"node_id,omitempty"`
	User        string `json:"user"`
	AccessID    string `json:"access_id"`
	TopologyUID string `json:"topology_uid"`
	ResourceUID string `json:"resource_uid"`
	Generation  int64  `json:"generation"`
}

func (q SSHAccessQuery) Validate() error {
	if ValidateSSHAccessUser(q.User) != nil || !fileInstallID.MatchString(q.AccessID) ||
		q.TopologyUID == "" || q.ResourceUID == "" || q.Generation < 1 ||
		strings.ContainsRune(q.TopologyUID+q.ResourceUID+q.NodeID, 0) {
		return fmt.Errorf("complete managed SSH discovery identity required")
	}
	return nil
}

// SSHAccessDiscovery describes current configuration, not service readiness.
// An absent result with a Current tombstone retains its monotonic revision.
// No Current means this access has never been recorded in the observed store.
type SSHAccessDiscovery struct {
	Query   SSHAccessQuery   `json:"query"`
	BootID  string           `json:"boot_id"`
	Status  string           `json:"status"`
	Current *SSHAccessResult `json:"current,omitempty"`
	Config  *SSHAccessConfig `json:"config,omitempty"`
}

// Validate checks the response at each trusted transport boundary. The caller
// supplies its expected query; a response cannot choose a different target.
func (d SSHAccessDiscovery) Validate(query SSHAccessQuery) error {
	if query.Validate() != nil || d.Query != query || d.BootID == "" || (d.Status != "present" && d.Status != "absent") {
		return fmt.Errorf("SSH discovery identity or status mismatch")
	}
	if d.Current == nil {
		if d.Status != "absent" || d.Config != nil {
			return fmt.Errorf("SSH discovery lacks committed identity")
		}
		return nil
	}
	if d.Config == nil {
		return fmt.Errorf("SSH discovery configuration missing")
	}
	h := d.Current.Handle
	if h.NodeID != query.NodeID || h.TopologyUID != query.TopologyUID || h.ResourceUID != query.ResourceUID || h.Generation != query.Generation ||
		h.User != query.User || h.AccessID != query.AccessID || h.Revision == 0 || h.Epoch == 0 || h.BootID == "" || (d.Status == "present") != d.Current.Present {
		return fmt.Errorf("SSH discovery committed identity mismatch")
	}
	r := SSHAccessMutation{ID: h.ID, ExpectedRevision: h.Revision - 1, Delete: !d.Current.Present, Config: *d.Config}
	digest, err := r.Digest()
	if err != nil || digest != h.Digest || r.Config.User != h.User || r.Config.AccessID != h.AccessID {
		return fmt.Errorf("SSH discovery configuration digest mismatch")
	}
	return nil
}
