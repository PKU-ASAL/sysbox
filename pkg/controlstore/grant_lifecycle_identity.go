package controlstore

import (
	"github.com/oslab/sysbox/pkg/persistence"
)

// Preserve source-error precedence and the durable clock floor on rejection.
// A lifecycle conflict commits only ObserveTime, never a grant or lease write;
// actual storage errors still abort their transaction.
func (b AuthorityStorage) checkLifecycle(record *persistence.CommandRecord, check func(topology string) error) error {
	if b.Lifecycle == nil || b.SourceErr != nil && *b.SourceErr != nil {
		return nil
	}
	topology, err := GrantCommandTopology(record)
	if err == nil {
		err = check(topology)
	}
	if err == nil {
		return nil
	}
	return b.rejectGrantSource(err)
}

// A worker cannot supply another topology name for a retained command. Resolve
// it only from the durable dispatch payload and pair it with its immutable UID.
func GrantCommandTopology(record *persistence.CommandRecord) (string, error) {
	if record == nil || record.Command.TopologyUID == "" {
		return "", persistence.ErrConflict
	}
	c := record.Command
	topology := ""
	switch c.Type {
	case "run_assigned":
		if c.Run != nil {
			topology = c.Run.Topology
		}
	case "node_operation":
		topology = c.Operation.Topology
	case "guest_execution":
		if c.Execution != nil {
			topology = c.Execution.Topology
		}
	case "guest_file_put":
		if c.FilePut != nil {
			topology = c.FilePut.Topology
		}
	case "gc_delete", "session_open":
		if c.Session != nil {
			topology = c.Session.Topology
		}
	default:
		return "", persistence.ErrUnsupportedVersion
	}
	if err := ValidatePathSegment(topology, "topology"); err != nil {
		return "", persistence.ErrConflict
	}
	return topology, nil
}
