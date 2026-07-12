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

type observingNIC struct {
	result driver.AttachmentResult
	err    error
}

func (o observingNIC) Attach(context.Context, substrate.NodeHandle, driver.AttachmentRequest) (driver.AttachmentResult, error) {
	return driver.AttachmentResult{}, nil
}
func (o observingNIC) Observe(context.Context, substrate.NodeHandle, driver.AttachmentRequest, json.RawMessage) (driver.AttachmentResult, error) {
	return o.result, o.err
}
func (o observingNIC) Delete(context.Context, substrate.NodeHandle, driver.AttachmentRequest, json.RawMessage) error {
	return nil
}

func withObservingNIC(t *testing.T, nic driver.NIC) {
	t.Helper()
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "test", Version: "1", NIC: nic}))
}

func attachmentResource() state.Resource {
	return state.Resource{Address: address.Resource("sysbox_node", "web"), Driver: "test", Attachments: []state.Attachment{{Name: "uplink", Network: address.Resource("sysbox_network", "public"), MAC: "02:00:00:00:00:01", IPPrefixes: []string{"10.0.0.10/24"}, Observation: state.AttachmentObservation{GuestDevice: "eth1"}, DriverState: json.RawMessage(`{"id":"old"}`)}}}
}

func TestObserveAttachmentsUpdatesDeviceWithoutDrift(t *testing.T) {
	withObservingNIC(t, observingNIC{result: driver.AttachmentResult{GuestDevice: "eth7", State: json.RawMessage(`{"id":"new"}`)}})
	r := attachmentResource()
	status, _, err := observeAttachments(context.Background(), substrate.NodeHandle{ID: "node"}, &r)
	require.NoError(t, err)
	require.Equal(t, state.ResourcePresent, status)
	require.Equal(t, "eth7", r.Attachments[0].Observation.GuestDevice)
	require.JSONEq(t, `{"id":"new"}`, string(r.Attachments[0].DriverState))
}

func TestObserveAttachmentsClassifiesNotFoundAndUnavailable(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		withObservingNIC(t, observingNIC{err: driver.Wrap(driver.ErrorNotFound, "test", "missing", nil)})
		r := attachmentResource()
		status, _, err := observeAttachments(context.Background(), substrate.NodeHandle{}, &r)
		require.NoError(t, err)
		require.Equal(t, state.ResourceDrifted, status)
	})
	t.Run("unavailable", func(t *testing.T) {
		withObservingNIC(t, observingNIC{err: driver.Wrap(driver.ErrorUnavailable, "test", "offline", errors.New("down"))})
		r := attachmentResource()
		before := append([]byte(nil), r.Attachments[0].DriverState...)
		status, _, err := observeAttachments(context.Background(), substrate.NodeHandle{}, &r)
		require.Error(t, err)
		require.Equal(t, state.ResourceUnknown, status)
		require.Equal(t, before, []byte(r.Attachments[0].DriverState))
	})
}
