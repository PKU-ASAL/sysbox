package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestGuestExecutionDockerE2E is intentionally opt-in because it executes in
// an existing topology and requires a live API plus its owning Agent.
func TestGuestExecutionDockerE2E(t *testing.T) {
	apiURL, token := os.Getenv("SYSBOX_GUEST_E2E_API"), os.Getenv("SYSBOX_GUEST_E2E_TOKEN")
	topology, node := os.Getenv("SYSBOX_GUEST_E2E_TOPOLOGY"), os.Getenv("SYSBOX_GUEST_E2E_NODE")
	if apiURL == "" || token == "" || topology == "" || node == "" {
		t.Skip("requires SYSBOX_GUEST_E2E_API, SYSBOX_GUEST_E2E_TOKEN, SYSBOX_GUEST_E2E_TOPOLOGY, SYSBOX_GUEST_E2E_NODE and a running owning Agent")
	}
	request := func(method, path string, body []byte, out any) int {
		req, err := http.NewRequest(method, apiURL+path, bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		if out != nil {
			require.NoError(t, json.Unmarshal(raw, out), string(raw))
		}
		return resp.StatusCode
	}
	var execution struct {
		ID, Status string
		Result     struct {
			ExitCode         int
			Stdout, Encoding string
		}
	}
	path := fmt.Sprintf("/v1/topologies/%s/nodes/%s/executions", topology, node)
	require.Equal(t, http.StatusAccepted, request(http.MethodPost, path, []byte(`{"argv":["printf","guest-e2e"]}`), &execution))
	deadline := time.Now().Add(30 * time.Second)
	for !guestExecutionTerminal(execution.Status) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		request(http.MethodGet, "/v1/executions/"+execution.ID, nil, &execution)
	}
	require.Equal(t, "completed", execution.Status)
	require.Equal(t, 0, execution.Result.ExitCode)
	require.Equal(t, "base64", execution.Result.Encoding)
}
