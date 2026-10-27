package api

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestForceUnlockRefusesActiveRun(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	require.NoError(t, s.apiStore.SaveRun(context.Background(), controlplane.Run{
		ID: "active", Topology: "lab", Op: "apply", Status: controlplane.RunRunning,
		QueuedAt: time.Now().UTC(),
	}))
	recorder := httptest.NewRecorder()
	s.ServeHTTP(recorder, httptest.NewRequest("POST", "/v1/topologies/lab/state/force-unlock", nil))
	require.Equal(t, 409, recorder.Code, recorder.Body.String())
}
