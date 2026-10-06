package docker

import (
	"context"
	"encoding/json"
	"fmt"
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
	for _, name := range []string{"start", "stop", "destroy", "pause", "resume", "exec"} {
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
			}
			require.Error(t, err)
			require.Zero(t, mutations)
		})
	}
}
