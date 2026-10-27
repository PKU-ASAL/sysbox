package hostsupervisor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
)

func (c *GCCoordinator) gcAnchorPath(g controlplane.SignedExecutionGrant) string {
	return filepath.Join(c.Root, "gc-admitted-"+digestHex([]byte(g.Grant.Scope.TopologyUID))+".anchor")
}
func (c *GCCoordinator) checkGCJournalAnchor(g controlplane.SignedExecutionGrant) error {
	raw, err := os.ReadFile(c.gcAnchorPath(g))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var owner string
	if json.Unmarshal(raw, &owner) != nil || owner != g.Grant.Scope.TopologyUID {
		return effectgate.ErrUncertain
	}
	path := filepath.Join(c.Root, digestHex([]byte(owner))+".json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: admitted GC journal missing", effectgate.ErrUncertain)
	}
	return nil
}

// Called inside the persisted intent gate, before the first provider effect.
// A separate fsynced anchor keeps missing ledger distinguishable from no work.
func (c *GCCoordinator) anchorGCAdmission(g controlplane.SignedExecutionGrant) error {
	path := c.gcAnchorPath(g)
	raw, _ := json.Marshal(g.Grant.Scope.TopologyUID)
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) != string(raw) {
			return effectgate.ErrUncertain
		}
	} else if !os.IsNotExist(err) {
		return err
	} else {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.Write(raw)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	dir, err := os.Open(c.Root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
