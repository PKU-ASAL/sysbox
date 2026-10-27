//go:build e2e

package runtime_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/persistence"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	runtime "github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type unavailableFirewallOutcome struct{ runtime.ActionRepository }

func (r unavailableFirewallOutcome) CommitActionOutcome(context.Context, runtime.ActionRecord, *runtime.StatePatch) error {
	return errors.New("injected outcome persistence failure")
}

type observeOnlyFirewall struct{ *dockerprovider.Substrate }

func (p observeOnlyFirewall) ApplyRuleset(context.Context, driver.PolicyTarget, driver.RulesetSpec) (driver.RulesetObservation, error) {
	return driver.RulesetObservation{}, errors.New("recovery attempted to replay ApplyRuleset")
}

func TestFirewallRealRepositoryRecoveryE2E(t *testing.T) {
	if os.Getenv("SYSBOX_FIREWALL_E2E") != "1" {
		t.Skip("requires Docker socket, host PID visibility and namespace administration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sub, err := dockerprovider.New()
	require.NoError(t, err)
	defer sub.Close()
	root := t.TempDir()
	repo, err := persistence.OpenFilesystem(root)
	require.NoError(t, err)
	defer repo.Close()
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	nodeID := substrate.OperationIdentity{ActionID: "fixture", OwnerUID: st.TopologyUID, TopologyUID: st.TopologyUID, ResourceUID: uuid.NewString(), Generation: 1}
	nodeCtx := substrate.WithOperationIdentity(ctx, nodeID)
	h, err := sub.CreateNode(nodeCtx, substrate.NodeSpec{Name: "sysbox-firewall-e2e-" + uuid.NewString(), Image: substrate.ArtifactHandle{ID: "alpine:3.22"}, Sysctls: map[string]string{"net.ipv4.ip_forward": "0"}})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		require.NoError(t, sub.DestroyNode(substrate.WithOperationIdentity(cleanup, nodeID), h))
	})
	require.NoError(t, sub.StartNode(nodeCtx, h))
	st.AddResource(state.Resource{Address: address.Resource("sysbox_node", "target"), Driver: "docker", ExternalID: h.ID, OwnerUID: nodeID.OwnerUID, ResourceUID: nodeID.ResourceUID, Generation: 1})
	raw, err := st.Marshal()
	require.NoError(t, err)
	require.NoError(t, repo.StateBackend("lab").(state.VersionedBackend).SaveVersioned(ctx, raw, state.SaveOptions{RequireCAS: true, ExpectedSerial: st.Meta.Serial}))
	st, err = repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	hcl := `resource "sysbox_image" "base" {
  substrate = "docker"
  source = "alpine:3.22"
  kind = "oci"
  architecture = "amd64"
  guest_family = "linux"
}
resource "sysbox_node" "target" {
  substrate = "docker"
  image = "sysbox_image.base.id"
}
resource "sysbox_firewall" "edge" {
  attach_to = "sysbox_node.target"
  default_input = "drop"
}
`
	parsed, err := config.ParseString(hcl, "firewall-e2e.hcl")
	require.NoError(t, err)
	eval, err := config.BuildEvalContext(parsed)
	require.NoError(t, err)
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	defer func() { driver.DefaultRegistry = previous }()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "test", Policy: sub, Node: sub, NIC: sub, NodeState: sub}))
	g, err := runtime.BuildGraph(parsed, eval)
	require.NoError(t, err)
	a := address.Resource("sysbox_firewall", "edge")
	plan := &runtime.Plan{Actions: []controlplane.PlannedChange{{Address: a, Action: controlplane.PlanActionCreate}}}
	e := runtime.NewExecutor(g, st)
	e.SetRunContext("lab", "create")
	e.SetActionRepository(repo)
	require.NoError(t, e.Apply(ctx, plan), "ordinary firewall creation must not require router forwarding")
	deletePlan := &runtime.Plan{Actions: []controlplane.PlannedChange{{Address: a, Action: controlplane.PlanActionDelete}}}
	require.NoError(t, e.Destroy(ctx, deletePlan))
	e.SetActionRepository(unavailableFirewallOutcome{repo})
	require.Error(t, e.Apply(ctx, plan))
	reopened, err := persistence.OpenFilesystem(root)
	require.NoError(t, err)
	defer reopened.Close()
	current, err := reopened.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.Nil(t, current.FindResource(a))
	driver.DefaultRegistry = driver.NewRegistry()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "test", Policy: observeOnlyFirewall{sub}}))
	recovery := runtime.NewExecutor(g, current)
	recovery.SetRunContext("lab", "recovery")
	recovery.SetActionRepository(reopened)
	configuration := g.Get(a).Data.(*config.FirewallConfig)
	configuration.DefaultInput = "accept"
	require.ErrorIs(t, recovery.RecoverActions(ctx, plan), runtime.ErrPlanStale)
	require.Nil(t, current.FindResource(a), "changed input must not be adopted")
	configuration.DefaultInput = "drop"
	require.NoError(t, recovery.RecoverActions(ctx, plan))
	require.NotNil(t, current.FindResource(a))
	require.NoError(t, recovery.Destroy(ctx, deletePlan))
	t.Log("HCL -> default engine -> real Docker/nft -> durable intent -> reopened repository recovery without Apply replay -> destroy passed")
}
