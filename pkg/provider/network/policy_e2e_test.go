//go:build e2e

package network

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/driver"
)

func TestOwnedPolicyRepeatedApplyAndDeleteE2E(t *testing.T) {
	requirePolicyE2E(t)
	namespace := fmt.Sprintf("sysbox-e2e-policy-%d", os.Getpid())
	require.NoError(t, CreateNetns(namespace))
	t.Cleanup(func() { _ = DeleteNetns(namespace) })
	targetRaw, err := json.Marshal(policyTargetState{Namespace: namespace, Bindings: map[string]string{}})
	require.NoError(t, err)
	target := driver.PolicyTarget{Resource: "sysbox_firewall.edge", State: targetRaw}
	spec := driver.RulesetSpec{Owner: "e2e/sysbox_firewall.edge", Family: driver.FamilyIPv4}
	provider := Driver{}

	first, err := provider.ApplyRuleset(context.Background(), target, spec)
	require.NoError(t, err)
	second, err := provider.ApplyRuleset(context.Background(), target, spec)
	require.NoError(t, err)
	require.Equal(t, first.Table, second.Table)
	require.Equal(t, first.Digest, second.Digest)
	require.Equal(t, len(first.Inventory), len(second.Inventory))

	require.NoError(t, provider.DeleteRuleset(context.Background(), target, spec.Owner))
	_, err = provider.ObserveRuleset(context.Background(), target, spec.Owner)
	require.True(t, driver.IsCategory(err, driver.ErrorNotFound), err)
}

func requirePolicyE2E(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("owned nftables policy e2e requires root/CAP_NET_ADMIN")
	}
	probe := fmt.Sprintf("sysbox-e2e-policy-cap-%d", os.Getpid())
	if err := CreateNetns(probe); err != nil {
		t.Skipf("owned nftables policy e2e requires CAP_NET_ADMIN: %v", err)
	}
	require.NoError(t, DeleteNetns(probe))
}
