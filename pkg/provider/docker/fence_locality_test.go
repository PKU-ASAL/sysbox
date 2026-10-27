package docker

import (
	"context"
	"encoding/json"
	"github.com/docker/docker/client"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestFenceLocalityRejectsRemoteDaemon(t *testing.T) {
	for _, host := range []string{"unix:///var/run/docker.sock", "tcp://127.0.0.1:2375"} {
		cli, err := client.NewClientWithOpts(client.WithHost(host))
		require.NoError(t, err)
		defer cli.Close()
		s := &Substrate{cli: cli}
		scope, err := s.HostFenceScope(context.Background())
		if host == "unix:///var/run/docker.sock" {
			require.NoError(t, err)
			require.Equal(t, "local-only", scope)
		} else {
			require.Error(t, err)
		}
	}
}

func TestFenceAttachmentAbsenceChecksEndpointAndRejectsUnknownState(t *testing.T) {
	for _, mode := range []string{"absent", "endpoint", "unavailable", "missing-proof", "conflict", "network-absent"} {
		t.Run(mode, func(t *testing.T) {
			cli, err := client.NewClientWithOpts(client.WithHost("unix:///var/run/docker.sock"), client.WithVersion("1.41"), client.WithHTTPClient(&http.Client{Transport: fenceRoundTripper(func(r *http.Request) (*http.Response, error) {
				status, body := 404, `{"message":"missing container"}`
				if strings.Contains(r.URL.Path, "/networks/") {
					status, body = 200, `{"Id":"net","Containers":{}}`
					if mode == "endpoint" {
						body = `{"Id":"net","Containers":{"container":{"Name":"old"}}}`
					}
					if mode == "unavailable" {
						status, body = 503, `{"message":"offline"}`
					}
					if mode == "network-absent" {
						status, body = 404, `{"message":"missing network"}`
					}
					if mode == "conflict" {
						body = `{"Id":"foreign","Containers":{}}`
					}
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}))
			require.NoError(t, err)
			defer cli.Close()
			raw := json.RawMessage(`{"kind":"docker-nat","network_id":"net"}`)
			if mode == "missing-proof" {
				raw = json.RawMessage(`{}`)
			}
			ctx := substrate.WithOperationIdentity(context.Background(), substrate.OperationIdentity{ActionID: "delete", OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "node", Generation: 1})
			err = (&Substrate{cli: cli}).HostFenceAttachmentAbsent(ctx, substrate.NodeHandle{ID: "container"}, "lan", raw)
			if mode == "absent" || mode == "network-absent" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

type fenceRoundTripper func(*http.Request) (*http.Response, error)

func (f fenceRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
