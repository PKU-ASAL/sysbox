package libvirt

import (
	"context"

	"github.com/oslab/sysbox/pkg/substrate"
)

// AttachNIC records the bridge in HandleState.Bridges so that StartNode
// can include the interface in the domain XML. Libvirt creates the TAP
// device and attaches it to the bridge when the domain starts.
func (s *Substrate) AttachNIC(_ context.Context, h substrate.NodeHandle, req substrate.LinkRequest) (substrate.AttachedNIC, error) {
	hs := hsFrom(h)
	hs.Bridges = append(hs.Bridges, BridgeAttach{Bridge: req.Bridge})
	return substrate.AttachedNIC{
		Kind: substrate.NICKindTap,
		IP:   req.IP,
	}, nil
}
