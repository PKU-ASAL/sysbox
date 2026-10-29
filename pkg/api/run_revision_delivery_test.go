package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestDestroyPinsPublishedRevisionBeforeDispatch(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "field.sysbox.hcl")
	require.NoError(t, os.WriteFile(path, []byte("# captured destroy"), 0600))
	run := controlplane.Run{ID: "destroy", Topology: "lab", Op: "destroy", SnapshotPath: path}
	require.NoError(t, s.runService.pinDestroyRevision(context.Background(), &run))
	revision, err := s.apiStore.GetGlobalRevision(context.Background(), run.Revision)
	require.NoError(t, err)
	require.Equal(t, []byte("# captured destroy"), revision.Files["field.sysbox.hcl"])
	require.NotEqual(t, path, run.SnapshotPath)
}

func TestRunRevisionDeliveryRequiresCurrentClaim(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	require.NoError(t, s.apiStore.SaveAgent(ctx, controlplane.Agent{ID: "owner", AuthSecret: "delivery-secret", SecretHash: agent.SecretHash("delivery-secret")}))
	revision := publishRevision(t, s, "# immutable tree")
	run := controlplane.Run{ID: "delivery", Topology: "lab", AgentID: "owner", Op: "apply", Status: controlplane.RunAssigned, QueuedAt: time.Now().UTC(), Revision: revision, SnapshotPath: "/api-only/field.sysbox.hcl"}
	require.NoError(t, s.apiStore.SaveRun(ctx, run))
	claimed, ok, err := s.apiStore.ClaimRun(ctx, run.ID, "owner", "lease", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	for _, tc := range []struct {
		name   string
		stale  bool
		status int
	}{{"current", false, 200}, {"stale attempt", true, 409}, {"unsigned", false, 401}} {
		t.Run(tc.name, func(t *testing.T) {
			proof := *claimed
			if tc.stale {
				proof.Attempt++
			}
			body, err := json.Marshal(proof)
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			request := httptest.NewRequest("POST", "/v1/agents/owner/runs/delivery/revision", bytes.NewReader(body))
			if tc.name != "unsigned" {
				require.NoError(t, agent.SignRequest(request, "owner", "delivery-secret", time.Now()))
			}
			s.ServeHTTP(rec, request)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			if tc.status == 200 {
				var result controlplane.GlobalRevision
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
				require.Equal(t, revision, controlplane.ComputeRevisionDigest(result.Files))
			}
		})
	}
}
