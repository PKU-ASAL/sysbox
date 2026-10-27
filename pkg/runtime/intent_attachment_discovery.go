package runtime

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

func discoverIntentAttachments(ctx context.Context, pc *ProviderContext, n *graph.Node, h substrate.NodeHandle, provider string, node driver.Node) ([]state.Attachment, error) {
	var inputs []AttachmentInput
	switch cfg := n.Data.(type) {
	case *config.NodeConfig:
		for _, l := range cfg.Links {
			inputs = append(inputs, AttachmentInput{Name: l.Name, Network: l.Network, MAC: l.MAC, IPPrefixes: []string{l.IP}, Gateway: l.Gateway, Aliases: l.Aliases})
		}
	case *config.RouterConfig:
		for _, l := range cfg.Interfaces {
			inputs = append(inputs, AttachmentInput{Name: l.Name, Network: l.Network, IPPrefixes: []string{l.IP}})
		}
	default:
		return nil, ErrObservationUnknown
	}
	discover, ok := node.(driver.ManagedAttachmentDiscovery)
	if !ok {
		return nil, ErrObservationUnknown
	}
	intents, err := NormalizeAttachmentIntents(pc.Topology(), n.Address, inputs)
	if err != nil {
		return nil, err
	}
	attachments := make([]state.Attachment, 0, len(intents))
	requests := make([]driver.AttachmentRequest, 0, len(intents))
	for _, in := range intents {
		a := state.Attachment{Name: in.Name, Node: n.Address, Network: in.Network, MAC: in.MAC, IPPrefixes: in.IPPrefixes, Gateway: in.Gateway, Driver: provider}
		network := pc.exec.state.FindResource(in.Network)
		if network == nil {
			return nil, ErrObservationUnknown
		}
		if network.IsNAT() && h.Conn.Kind == substrate.ConnKindDocker {
			a.Aliases = in.Aliases
		} else if in.AliasesExplicit {
			return nil, ErrObservationUnknown
		}
		req, err := attachmentRequestFromState(pc.exec.state, a)
		if err != nil {
			return nil, err
		}
		requests = append(requests, req)
		attachments = append(attachments, a)
	}
	found, err := discover.DiscoverManagedAttachments(ctx, h, requests)
	if err != nil {
		return nil, err
	}
	if len(found) != len(attachments) {
		return nil, fmt.Errorf("incomplete attachment inventory")
	}
	for i := range attachments {
		got, ok := found[attachments[i].Name]
		if !ok || got.Driver != provider || len(got.State) == 0 {
			return nil, ErrObservationUnknown
		}
		attachments[i].DriverState = got.State
		attachments[i].Observation.GuestDevice = got.GuestDevice
	}
	return attachments, nil
}
