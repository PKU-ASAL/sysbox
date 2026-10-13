//go:build e2e

package docker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type loseDeleteResponse struct {
	base   http.RoundTripper
	target string
	cancel context.CancelFunc
	calls  atomic.Int32
}

func (p *loseDeleteResponse) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := p.base.RoundTrip(req)
	if err == nil && req.Method == http.MethodDelete && strings.HasSuffix(req.URL.Path, "/containers/"+p.target) && req.URL.Query().Get("force") == "1" && response.StatusCode == http.StatusNoContent {
		p.calls.Add(1)
		response.Body.Close()
		p.cancel()
		return nil, io.ErrUnexpectedEOF
	}
	return response, err
}

func TestDockerRealLostDeleteResponseE2E(t *testing.T) {
	if os.Getenv("SYSBOX_REMOTE_DOCKER_E2E") != "1" {
		t.Skip("requires real Docker daemon")
	}
	ctx, done := context.WithTimeout(context.Background(), time.Minute)
	defer done()
	s, err := New()
	require.NoError(t, err)
	defer s.Close()
	image, _, err := s.cli.ImageInspectWithRaw(ctx, "alpine:3.22")
	require.NoError(t, err)
	owner := fmt.Sprintf("lost-delete-%d", time.Now().UnixNano())
	identity := substrate.OperationIdentity{ActionID: "create", OwnerUID: owner, TopologyUID: owner, ResourceUID: "node", Generation: 1}
	managed := substrate.WithOperationIdentity(ctx, identity)
	h, err := s.CreateNode(managed, substrate.NodeSpec{Name: owner, Image: substrate.ArtifactHandle{ID: image.ID}, ManagedNetwork: true})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := s.cli.ContainerInspect(cleanup, h.ID); errdefs.IsNotFound(err) {
			return
		}
		require.NoError(t, s.cli.ContainerRemove(cleanup, h.ID, container.RemoveOptions{Force: true}))
	})
	call, cancel := context.WithCancel(managed)
	defer cancel()
	httpClient := s.cli.HTTPClient()
	base := httpClient.Transport
	proxy := &loseDeleteResponse{base: base, target: h.ID, cancel: cancel}
	httpClient.Transport = proxy
	faultClient, err := client.NewClientWithOpts(client.WithHost(s.cli.DaemonHost()), client.WithVersion(s.cli.ClientVersion()), client.WithHTTPClient(httpClient))
	require.NoError(t, err)
	defer faultClient.Close()
	faultProvider := &Substrate{cli: faultClient}
	require.NoError(t, faultProvider.DestroyNode(call, h))
	require.ErrorIs(t, call.Err(), context.Canceled)
	require.EqualValues(t, 1, proxy.calls.Load())
	_, err = s.cli.ContainerInspect(ctx, h.ID)
	require.True(t, errdefs.IsNotFound(err), "%v", err)
	t.Log("real removal response lost and caller canceled; immutable-ID absence confirmed without retrying DELETE")
}
