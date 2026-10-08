package agentexec

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/util"
)

// acceptCommand persists only execution identity, never request bodies or
// sensitive inputs. An existing record is deliberately not replay permission:
// a crash may have occurred after an external effect and before its receipt.
func acceptCommand(path, session string, cmd *controlplane.AgentCommand) error {
	if path == "" || session == "" || cmd == nil || cmd.ID == "" {
		return fmt.Errorf("durable inbox identity required")
	}
	if err := util.MkdirAllDurable(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return fmt.Errorf("command outcome unconfirmed; checkpoint recovery required")
	}
	if err != nil {
		return err
	}
	defer f.Close()
	record := struct{ ID, Type, Session string }{cmd.ID, cmd.Type, session}
	if err = json.NewEncoder(f).Encode(record); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func commandInboxKey(id string) string { return fmt.Sprintf("command-%x", sha256.Sum256([]byte(id))) }
