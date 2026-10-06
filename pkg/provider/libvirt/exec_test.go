package libvirt

import (
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
