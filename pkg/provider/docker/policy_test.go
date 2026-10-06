package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

func TestResolvePolicyDeviceMatchesAddressWithoutGuestTooling(t *testing.T) {
	interfaces := []policyInterface{
		{Name: "lo", Addresses: []*net.IPNet{{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)}}},
		{Name: "eth0", Addresses: []*net.IPNet{{IP: net.ParseIP("10.0.5.30"), Mask: net.CIDRMask(24, 32)}}},
	}

	device, err := resolvePolicyDevice("10.0.5.30/24", interfaces)
	require.NoError(t, err)
	require.Equal(t, "eth0", device)

	_, err = resolvePolicyDevice("10.0.5.31/24", interfaces)
	require.ErrorContains(t, err, "no interface has IP 10.0.5.31")
}

func TestPolicyRejectsTargetIdentityConflictBeforeNamespaceAccess(t *testing.T) {
	for _, fault := range []string{"generation", "owner", "resource", "topology", "id", "missing"} {
		t.Run(fault, func(t *testing.T) {
			expected := testOperation()
			actual := expected
			containerID := "exact-id"
			switch fault {
			case "generation":
				actual.Generation++
			case "owner":
				actual.OwnerUID = "foreign"
			case "resource":
				actual.ResourceUID = "foreign"
			case "topology":
				actual.TopologyUID = "foreign"
			case "id":
				containerID = "replacement-id"
			case "missing":
				expected = substrate.OperationIdentity{}
			}
			s := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "GET", r.Method)
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": containerID, "Config": map[string]any{"Labels": operationLabels(actual)}, "State": map[string]any{"Running": true, "Pid": 99999999}})
			})
			raw, err := json.Marshal(map[string]any{"container_id": "exact-id", "identity": expected})
			require.NoError(t, err)
			target := driver.PolicyTarget{Resource: "sysbox_router.node", Identity: expected, State: raw}
			_, err = s.ApplyRuleset(context.Background(), target, driver.RulesetSpec{Owner: "policy"})
			require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
			_, err = s.ObserveRuleset(context.Background(), target, "policy")
			require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
			err = s.DeleteRuleset(context.Background(), target, "policy")
			require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
		})
	}
}

func TestDecodeDockerPolicyTargetRequiresContainerAndBindings(t *testing.T) {
	_, err := decodeDockerPolicyTarget(driver.PolicyTarget{Resource: "sysbox_router.node", Identity: testOperation(), State: json.RawMessage(`{"bindings":{}}`)})
	require.ErrorContains(t, err, "container_id")

	raw, err := json.Marshal(dockerPolicyTarget{ContainerID: "router-1", Identity: testOperation(), Bindings: map[string]string{"inside": "eth1", "uplink": "eth0"}})
	require.NoError(t, err)
	target, err := decodeDockerPolicyTarget(driver.PolicyTarget{Resource: "sysbox_router.node", Identity: testOperation(), State: raw})
	require.NoError(t, err)
	require.Equal(t, "router-1", target.ContainerID)
	require.Equal(t, "eth1", target.Bindings["inside"])
}

func TestPolicyNamespaceRechecksContainerBeforeUse(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			calls := 0
			s := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				pid := os.Getpid()
				if changed && calls > 1 {
					pid++
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": "id", "Config": map[string]any{"Labels": operationLabels(testOperation())}, "State": map[string]any{"Running": true, "Pid": pid}})
			})
			ns, err := s.policyTargetNamespace(context.Background(), dockerPolicyTarget{ContainerID: "id", Identity: testOperation()})
			if changed {
				require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
				require.Nil(t, ns)
			} else {
				require.NoError(t, err)
				defer ns.Close()
				actual, err := ns.Stat()
				require.NoError(t, err)
				expected, err := os.Stat("/proc/self/ns/net")
				require.NoError(t, err)
				require.True(t, os.SameFile(actual, expected))
			}
			require.Equal(t, 2, calls)
		})
	}
}
