package controlstore

import (
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

// Called under the same authority lock as cancellation and grant mutation.
func (b AuthorityStorage) checkConsoleSource(record *persistence.CommandRecord, read func(string) (*controlplane.ConsoleSession, error)) error {
	if b.Lifecycle == nil || record == nil || record.Command.Type != "session_open" || b.SourceErr != nil && *b.SourceErr != nil {
		return nil
	}
	c := record.Command
	if c.Session == nil {
		return b.rejectGrantSource(persistence.ErrConflict)
	}
	s, err := read(c.Session.ID)
	if err == nil && (s == nil || persistence.ValidateConsoleSessionCAS(*c.Session, *s) != nil || s.TopologyUID != c.TopologyUID || s.ResourceUID != c.ResourceUID || s.Generation != c.Generation || s.HostCompletion != "" || s.Status != controlplane.ConsoleSessionStatusQueued && s.Status != controlplane.ConsoleSessionStatusRunning) {
		err = persistence.ErrConflict
	}
	return b.rejectGrantSource(err)
}
