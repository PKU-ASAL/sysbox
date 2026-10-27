package docker

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"net/http"
	"strings"
	"testing"
)

func TestAttachmentVerifiesNetworkGenerationIndependently(t *testing.T) {
	node := testOperation()
	target := node
	target.ResourceUID = "target-network"
	target.Generation = 7
	for _, generation := range []int64{0, 6, 7} {
		t.Run(string(rune('0'+generation)), func(t *testing.T) {
			effects := 0
			s := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					effects++
					w.WriteHeader(200)
					return
				}
				if strings.Contains(r.URL.Path, "/networks/") {
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "network-id", "Labels": operationLabels(target)})
				} else {
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "container-id", "Config": map[string]any{"Labels": operationLabels(node)}})
				}
			})
			expected := target
			expected.Generation = generation
			_, err := s.Attach(substrate.WithOperationIdentity(context.Background(), node), substrate.NodeHandle{ID: "container-id", Generation: node.Generation}, driver.AttachmentRequest{NetworkIdentity: expected, Name: "nic", IPPrefixes: []string{"10.0.0.2/24"}, NetworkState: json.RawMessage(`{"nat":true,"docker_network_id":"network-id"}`)})
			if generation == 7 {
				require.NoError(t, err)
				require.Equal(t, 1, effects)
			} else {
				require.Error(t, err)
				require.Zero(t, effects)
			}
		})
	}
}
