package guesttask

import (
	"context"
	"os"

	"github.com/oslab/sysbox/pkg/substrate"
)

func (e *Engine) DiscoverSSHAccess(ctx context.Context, query substrate.SSHAccessQuery) (substrate.SSHAccessDiscovery, error) {
	unknown := substrate.SSHAccessDiscovery{Query: query, Status: "unknown"}
	if err := ctx.Err(); err != nil {
		return unknown, err
	}
	if err := query.Validate(); err != nil {
		return unknown, err
	}
	if query.NodeID != "" {
		return unknown, ErrConflict
	}
	boot, err := BootID()
	if err != nil {
		return unknown, err
	}
	absent := substrate.SSHAccessDiscovery{Query: query, BootID: boot, Status: "absent"}
	parent, err := openInstallRoot(e.sshAccessStore().Root)
	if os.IsNotExist(err) {
		return absent, nil
	}
	if err != nil {
		return unknown, err
	}
	defer parent.Close()
	record, err := e.readSSHAccess(parent, sshAccessName(query.User, query.AccessID))
	if os.IsNotExist(err) {
		return absent, nil
	}
	if err != nil {
		return unknown, err
	}
	current := record.current()
	h := current.Result.Handle
	if h.TopologyUID != query.TopologyUID || h.ResourceUID != query.ResourceUID || h.Generation != query.Generation || h.User != query.User || h.AccessID != query.AccessID {
		return unknown, ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return unknown, err
	}
	status := "absent"
	if current.Result.Present {
		status = "present"
	}
	return substrate.SSHAccessDiscovery{Query: query, BootID: boot, Status: status, Current: &current.Result, Config: &current.Request.Config}, nil
}
