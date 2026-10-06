package network

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
)

func networkObservationFixture(t *testing.T) (Driver, context.Context, driver.IsolatedNetworkSpec) {
	t.Helper()
	d := Driver{ownershipRoot: t.TempDir(), namespaceRoot: t.TempDir()}
	d.linkLookup = func(string, string) (netlink.Link, error) { return nil, netlink.LinkNotFoundError{} }
	id := substrate.OperationIdentity{ActionID: "observe", OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "owner/network", Generation: 1}
	return d, substrate.WithOperationIdentity(context.Background(), id), driver.IsolatedNetworkSpec{Name: "test-net", Bridge: "test-br", CIDR: "10.0.0.1/24"}
}
func TestIsolatedObservationProvesAbsence(t *testing.T) {
	d, ctx, spec := networkObservationFixture(t)
	observed, err := d.ObserveIsolated(ctx, spec)
	require.NoError(t, err)
	require.False(t, observed.Exists)
}
func TestIsolatedObservationQueryFailureIsNotAbsence(t *testing.T) {
	d, ctx, spec := networkObservationFixture(t)
	spec.RootBridge = "root-br"
	want := errors.New("netlink permission denied")
	d.linkLookup = func(string, string) (netlink.Link, error) { return nil, want }
	_, err := d.ObserveIsolated(ctx, spec)
	require.ErrorIs(t, err, want)
}
func TestIsolatedObservationRejectsUnownedNamespace(t *testing.T) {
	d, ctx, spec := networkObservationFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(d.namespaceRoot, spec.Name), []byte("namespace fixture"), 0600))
	_, err := d.ObserveIsolated(ctx, spec)
	require.Error(t, err)
}
func TestIsolatedObservationVerifiesAnchorAndConfiguration(t *testing.T) {
	d, ctx, spec := networkObservationFixture(t)
	path := filepath.Join(d.namespaceRoot, spec.Name)
	require.NoError(t, os.WriteFile(path, []byte("namespace fixture"), 0600))
	info, err := os.Stat(path)
	require.NoError(t, err)
	id, _ := substrate.OperationIdentityFromContext(ctx)
	marker := networkOwnership{Version: 1, Identity: id, Spec: spec}
	marker.setNamespace(info)
	require.NoError(t, d.writeOwnership(spec.Name, marker))
	link := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: spec.Bridge, Alias: networkAlias(id), Flags: net.FlagUp}}
	d.linkLookup = func(string, string) (netlink.Link, error) { return link, nil }
	ipnet := &net.IPNet{IP: net.ParseIP("10.0.0.1"), Mask: net.CIDRMask(24, 32)}
	d.addressLookup = func(string, netlink.Link) ([]netlink.Addr, error) { return []netlink.Addr{{IPNet: ipnet}}, nil }
	observed, err := d.ObserveIsolated(ctx, spec)
	require.NoError(t, err)
	require.True(t, observed.Healthy)
	replacement := filepath.Join(d.namespaceRoot, "replacement")
	require.NoError(t, os.WriteFile(replacement, []byte("different namespace"), 0600))
	require.NoError(t, os.Rename(replacement, path))
	_, err = d.ObserveIsolated(ctx, spec)
	require.ErrorContains(t, err, "identity")
}
func TestIsolatedMutationRequiresDurableOwner(t *testing.T) {
	d, _, spec := networkObservationFixture(t)
	require.Error(t, d.CreateIsolated(context.Background(), spec))
	require.Error(t, d.DeleteIsolated(context.Background(), spec))
}

func TestAbsentNetworkStillChecksDurableOwnership(t *testing.T) {
	for _, operation := range []string{"observe", "create", "delete"} {
		t.Run(operation, func(t *testing.T) {
			d, ctx, spec := networkObservationFixture(t)
			id, _ := substrate.OperationIdentityFromContext(ctx)
			id.Generation++
			require.NoError(t, d.writeOwnership(spec.Name, networkOwnership{Version: 1, Identity: id, Spec: spec}))
			before, err := os.ReadFile(d.ownershipPath(spec.Name))
			require.NoError(t, err)
			switch operation {
			case "observe":
				_, err = d.ObserveIsolated(ctx, spec)
			case "create":
				err = d.CreateIsolated(ctx, spec)
			case "delete":
				err = d.DeleteIsolated(ctx, spec)
			}
			require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
			after, err := os.ReadFile(d.ownershipPath(spec.Name))
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestDeleteAbsentNetworkRetiresMatchingOwnership(t *testing.T) {
	d, ctx, spec := networkObservationFixture(t)
	id, _ := substrate.OperationIdentityFromContext(ctx)
	require.NoError(t, d.writeOwnership(spec.Name, networkOwnership{Version: 1, Identity: id, Spec: spec}))
	require.NoError(t, d.DeleteIsolated(ctx, spec))
	require.NoFileExists(t, d.ownershipPath(spec.Name))
	require.NoError(t, d.DeleteIsolated(ctx, spec))
}

func TestAbsentNetworkRejectsCorruptAnchor(t *testing.T) {
	d, ctx, spec := networkObservationFixture(t)
	require.NoError(t, os.WriteFile(d.ownershipPath(spec.Name), []byte("{"), 0600))
	_, err := d.ObserveIsolated(ctx, spec)
	require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
}
