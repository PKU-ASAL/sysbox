package commands

import (
	"bytes"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestImportInspectionAndConfirmationUsePublicAPI(t *testing.T) {
	calls := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		require.Equal(t, "operation-key", r.Header.Get("Idempotency-Key"))
		if len(calls) == 1 {
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "docker", body["substrate"])
			require.Equal(t, "external", body["id"])
		}
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"id":"operation"}`))
	}))
	defer server.Close()
	inspect := newImportInspectCommand()
	inspect.SetArgs([]string{"sysbox_node.web", "external", "--api", server.URL, "--topology", "lab", "--substrate", "docker", "--key", "operation-key"})
	inspect.SetOut(new(bytes.Buffer))
	require.NoError(t, inspect.Execute())
	confirm := newImportConfirmCommand()
	confirm.SetArgs([]string{"inspect-one", "--api", server.URL, "--topology", "lab", "--key", "operation-key"})
	confirm.SetOut(new(bytes.Buffer))
	require.NoError(t, confirm.Execute())
	require.Equal(t, []string{"/v1/topologies/lab/adoption-inspections", "/v1/topologies/lab/adoption-inspections/inspect-one/confirm"}, calls)
}
