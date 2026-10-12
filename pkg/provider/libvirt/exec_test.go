package libvirt

import (
	"context"
	"reflect"
	"testing"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestConnectionBindsExecutionIdentity(t *testing.T) {
	sub := &Substrate{}
	conn, err := sub.Connection(substrate.NodeHandle{ID: "domain", Generation: 9, Net: substrate.NetInfo{PrimaryIP: "192.0.2.1"}}, nil)
	require.NoError(t, err)
	identity := reflect.ValueOf(conn.(*domainConnection).ssh).Elem().FieldByName("executionIdentity")
	require.Equal(t, "domain", identity.FieldByName("nodeID").String())
	require.EqualValues(t, 9, identity.FieldByName("generation").Int())
}

func TestDomainConnectionOffersManagedSSHProtocol(t *testing.T) {
	_, ok := any(&domainConnection{}).(substrate.ConnectionSSHAccess)
	require.True(t, ok)
}

func TestDomainSSHProtocolRejectsStaleGenerationBeforeTransport(t *testing.T) {
	actual := managedOperation()
	old := actual
	old.Generation--
	handle := managedNodeHandle(&HandleState{DomainName: "node", DomainUUID: "11111111-2222-4333-8444-555555555555", Identity: &actual})
	connection := &domainConnection{handle: handle}
	ctx := substrate.WithOperationIdentity(context.Background(), old)
	_, err := connection.DiscoverSSHAccess(ctx, substrate.SSHAccessQuery{})
	require.Error(t, err)
	_, err = connection.MutateSSHAccess(ctx, substrate.SSHAccessMutation{})
	require.Error(t, err)
	_, err = connection.ObserveSSHAccess(ctx, substrate.SSHAccessHandle{})
	require.Error(t, err)
	_, err = connection.InspectSSHAccess(ctx, substrate.SSHAccessHandle{})
	require.Error(t, err)
}
