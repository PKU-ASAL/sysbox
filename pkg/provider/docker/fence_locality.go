package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/oslab/sysbox/pkg/driver"
	ownednetwork "github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
)

// HostFenceScope is meaningful only inside an independently enrolled disposable
// host whose local sockets/storage are controlled by the acceptance controller.
func (s *Substrate) HostFenceScope(context.Context) (string, error) {
	if s == nil || s.cli == nil || s.cli.DaemonHost() != "unix:///var/run/docker.sock" {
		return "", fmt.Errorf("host fencing requires the local default Docker socket")
	}
	return "local-only", nil
}

func (s *Substrate) HostFenceDeletionAbsent(ctx context.Context, h substrate.NodeHandle) error {
	if _, err := s.HostFenceScope(ctx); err != nil {
		return err
	}
	if h.ID == "" {
		return fmt.Errorf("missing exact container identity")
	}
	observed, err := s.ObserveNode(ctx, h)
	if err != nil {
		return err
	}
	if observed.Status != substrate.NodeStatusMissing {
		return fmt.Errorf("container absence not proven")
	}
	return nil
}

// HostFenceAttachmentAbsent checks the exact persisted endpoint/link without
// mutation. An absent container alone cannot discharge independent host links.
func (s *Substrate) HostFenceAttachmentAbsent(ctx context.Context, h substrate.NodeHandle, name string, raw json.RawMessage) error {
	if err := s.HostFenceDeletionAbsent(ctx, h); err != nil {
		return err
	}
	var st attachmentState
	if json.Unmarshal(raw, &st) != nil || name == "" {
		return fmt.Errorf("missing attachment identity")
	}
	if st.NetworkID != "" {
		if st.Link != nil {
			return fmt.Errorf("conflicting attachment identity")
		}
		found, err := s.cli.NetworkInspect(ctx, st.NetworkID, network.InspectOptions{})
		if errdefs.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if found.ID != st.NetworkID {
			return fmt.Errorf("attachment network identity changed")
		}
		if _, present := found.Containers[h.ID]; present {
			return fmt.Errorf("container network endpoint remains")
		}
		return nil
	}
	if st.Link == nil {
		return fmt.Errorf("attachment ownership proof missing")
	}
	id, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok {
		return fmt.Errorf("attachment generation identity missing")
	}
	alias, err := ownednetwork.AttachmentAlias(id, name)
	if err != nil {
		return err
	}
	err = ownednetwork.ObserveOwnedAttachment(ctx, *st.Link, alias)
	if driver.IsCategory(err, driver.ErrorNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("owned attachment link remains")
}
