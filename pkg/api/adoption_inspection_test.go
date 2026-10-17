package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestAdoptionInspectionAdmissionIsReadOnlyAndAgentBound(t *testing.T) {
	s := guestExecutionTestServer(t)
	req := httptest.NewRequest("POST", "/v1/topologies/lab/adoption-inspections", bytes.NewBufferString(`{"type":"sysbox_node","name":"imported","id":"external","substrate":"docker"}`))
	req.Header.Set("Idempotency-Key", "inspect-one")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	require.Equal(t, 202, w.Code, w.Body.String())
	var op controlplane.NodeOperation
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &op))
	require.Equal(t, "adoption_inspect", op.Operation)
	require.NotNil(t, op.Inspection)
	require.Nil(t, op.InspectionEvidence)
	st, err := s.workspaceService().LoadState("lab")
	require.NoError(t, err)
	require.Len(t, st.Resources, 1)
	commands, err := s.apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1)
	inspected, err := controlplane.FromAdoptionInspectionCommand(commands[0])
	require.NoError(t, err)
	require.Equal(t, *op.Inspection, inspected)
}
