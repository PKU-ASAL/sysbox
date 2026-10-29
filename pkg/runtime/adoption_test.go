package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type adoptionTestProvider struct {
	repo    *memoryActions
	applied int
	owned   bool
}

func (*adoptionTestProvider) InspectAdoption(context.Context, string, substrate.NodeSpec) (substrate.AdoptionEvidence, error) {
	return substrate.AdoptionEvidence{}, nil
}
func (p *adoptionTestProvider) ApplyAdoption(context.Context, substrate.AdoptionEvidence) (substrate.NodeHandle, error) {
	if len(p.repo.actions) != 1 {
		return substrate.NodeHandle{}, errors.New("effect before intent")
	}
	p.applied++
	p.owned = true
	return substrate.NodeHandle{}, errors.New("lost provider response")
}
func (p *adoptionTestProvider) ObserveAdoption(context.Context, substrate.AdoptionEvidence) (substrate.NodeHandle, error) {
	if !p.owned {
		return substrate.NodeHandle{}, errors.New("not owned")
	}
	return substrate.NodeHandle{ID: "external", Generation: 1}, nil
}
func (*adoptionTestProvider) MarshalProviderState(substrate.NodeHandle) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (*adoptionTestProvider) UnmarshalProviderState(json.RawMessage) (any, error) { return nil, nil }
func TestAdoptionCommitsIntentBeforeEffectAndRecoversWithoutReplay(t *testing.T) {
	repo := &memoryActions{state: &state.State{TopologyUID: "owner"}}
	p := &adoptionTestProvider{repo: repo}
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	defer func() { driver.DefaultRegistry = previous }()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "adoption-test", Version: "1", OwnershipAdoption: p, NodeState: p}))
	id := substrate.OperationIdentity{ActionID: "action", OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "resource", Generation: 1}
	addr := address.Resource("sysbox_node", "web")
	evidence := substrate.AdoptionEvidence{Provider: "adoption-test", ExternalID: "external", Fingerprint: "fingerprint"}
	resource := state.Resource{Address: addr, Driver: evidence.Provider, ExternalID: evidence.ExternalID, OwnerUID: id.OwnerUID, ResourceUID: id.ResourceUID, Generation: 1}
	require.Error(t, CommitAdoption(context.Background(), repo, "run", "lab", addr, evidence, resource, id, 0, driver.DefaultRegistry))
	require.Equal(t, 1, p.applied)
	require.Empty(t, repo.state.Resources)
	require.NoError(t, CommitAdoption(context.Background(), repo, "run", "lab", addr, evidence, resource, id, 0, driver.DefaultRegistry))
	require.Equal(t, 1, p.applied)
	require.Len(t, repo.state.Resources, 1)
}
