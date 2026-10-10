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
