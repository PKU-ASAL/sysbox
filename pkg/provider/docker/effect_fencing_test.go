package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
	"time"
)

func TestManagedInspectUsesExecutionTimeout(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprint(background), func(t *testing.T) {
			s := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-r.Context().Done():
				case <-time.After(time.Second):
				}
			})
			conn, err := s.Connection(substrate.NodeHandle{ID: "node", Generation: 1}, nil)
			require.NoError(t, err)
			request := substrate.ExecRequest{Program: "true", Timeout: 30 * time.Millisecond}
			start := time.Now()
			if background {
				_, err = conn.(substrate.ConnectionStarter).Start(context.Background(), request)
			} else {
				_, err = conn.Exec(context.Background(), request, nil, nil)
			}
			require.Error(t, err)
			require.Less(t, time.Since(start), 500*time.Millisecond)
		})
	}
}

func TestDockerEffectsRejectUnidentifiedHandleBeforeAPI(t *testing.T) {
	calls := 0
	s := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(204) })
	ctx := context.Background()
	h := substrate.NodeHandle{ID: "unowned"}
	_, err := s.CreateNode(ctx, substrate.NodeSpec{Name: "unowned"})
	require.Error(t, err)
	for _, operation := range []func(context.Context, substrate.NodeHandle) error{s.StartNode, s.StopNode, s.Pause, s.Resume, s.DestroyNode} {
		require.Error(t, operation(ctx, h))
	}
	_, err = s.ExecInNode(ctx, h, substrate.ExecRequest{Program: "true"})
	require.Error(t, err)
	require.Zero(t, calls, "missing identity must not inspect or mutate arbitrary containers")
}

func TestManagedEffectsPinInspectedContainerID(t *testing.T) {
	var mutated string
	s := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "immutable-id", "Config": map[string]any{"Labels": operationLabels(testOperation())}})
			return
		}
		mutated = r.URL.Path
		w.WriteHeader(204)
	})
	require.NoError(t, s.StartNode(substrate.WithOperationIdentity(context.Background(), testOperation()), substrate.NodeHandle{ID: "reusable-name", Generation: 3}))
	require.Contains(t, mutated, "/containers/immutable-id/start")
	require.NotContains(t, mutated, "reusable-name")
}

func TestManagedAttachmentsPinInspectedContainerID(t *testing.T) {
	for _, operation := range []string{"attach", "detach"} {
		t.Run(operation, func(t *testing.T) {
			var containerID string
			s := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "immutable-id", "Config": map[string]any{"Labels": operationLabels(testOperation())}})
					return
				}
				var body struct{ Container string }
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				containerID = body.Container
				w.WriteHeader(200)
			})
			ctx := substrate.WithOperationIdentity(context.Background(), testOperation())
			h := substrate.NodeHandle{ID: "reusable-name", Generation: 3}
			if operation == "attach" {
				_, err := s.Attach(ctx, h, driver.AttachmentRequest{IPPrefixes: []string{"10.0.0.2/24"}, NetworkState: json.RawMessage(`{"nat":true,"docker_network_id":"network"}`)})
				require.NoError(t, err)
			} else {
				require.NoError(t, s.Delete(ctx, h, driver.AttachmentRequest{}, json.RawMessage(`{"network_id":"network"}`)))
			}
			require.Equal(t, "immutable-id", containerID)
		})
	}
}

func TestManagedAttachmentMissingContainerIsNotFound(t *testing.T) {
	s := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "GET", r.Method)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"container missing"}`))
	})
	h := substrate.NodeHandle{ID: "missing", Generation: 3}
	ctx := substrate.WithOperationIdentity(context.Background(), testOperation())
	_, err := s.Observe(ctx, h, driver.AttachmentRequest{}, json.RawMessage(`{"network_id":"network"}`))
	require.True(t, driver.IsCategory(err, driver.ErrorNotFound), "%v", err)
	err = s.Delete(ctx, h, driver.AttachmentRequest{}, json.RawMessage(`{"network_id":"network"}`))
	require.True(t, driver.IsCategory(err, driver.ErrorNotFound), "%v", err)
}
func TestManagedEffectRejectsMissingGenerationLabel(t *testing.T) {
	mutations := 0
	s := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			mutations++
			w.WriteHeader(204)
			return
		}
		labels := operationLabels(testOperation())
		delete(labels, "sysbox.generation")
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "immutable-id", "Config": map[string]any{"Labels": labels}})
	})
	require.Error(t, s.StopNode(context.Background(), substrate.NodeHandle{ID: "immutable-id", Generation: 3}))
	require.Zero(t, mutations)
}

func TestManagedEffectsRejectStaleGenerationBeforeMutation(t *testing.T) {
	for _, name := range []string{"start", "stop", "destroy", "pause", "resume", "exec", "attach", "detach", "observe-attachment"} {
		t.Run(name, func(t *testing.T) {
			mutations := 0
			s := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					mutations++
					w.WriteHeader(200)
					return
				}
				id := testOperation()
				id.Generation = 4
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": "immutable-id", "Config": map[string]any{"Labels": operationLabels(id)}})
			})
			ctx := substrate.WithOperationIdentity(context.Background(), testOperation())
			h := substrate.NodeHandle{ID: "name", Generation: 3}
			var err error
			switch name {
			case "start":
				err = s.StartNode(ctx, h)
			case "stop":
				err = s.StopNode(ctx, h)
			case "destroy":
				err = s.DestroyNode(ctx, h)
			case "pause":
				err = s.Pause(ctx, h)
			case "resume":
				err = s.Resume(ctx, h)
			case "exec":
				_, err = s.ExecInNode(ctx, h, substrate.ExecRequest{Program: "true"})
			case "attach":
				_, err = s.Attach(ctx, h, driver.AttachmentRequest{Name: "eth1", IPPrefixes: []string{"10.0.0.2/24"}, NetworkState: json.RawMessage(`{"nat":true,"docker_network_id":"network"}`)})
			case "detach":
				err = s.Delete(ctx, h, driver.AttachmentRequest{}, json.RawMessage(`{"network_id":"network"}`))
			case "observe-attachment":
				_, err = s.Observe(ctx, h, driver.AttachmentRequest{}, json.RawMessage(`{"host_end":"link","netns":"namespace"}`))
			}
			require.Error(t, err)
			require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
			require.Zero(t, mutations)
		})
	}
}
