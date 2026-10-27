package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestRecoveryInputsRequireExactResubmissionAfterAPIRestart(t *testing.T) {
	for _, kind := range []string{"file", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			cfg := config.MustLoadServiceConfig("")
			cfg.Paths.RunsDir = t.TempDir()
			cfg.Paths.WorkspacesDir = t.TempDir()
			switch kind {
			case "file":
				cfg.State.Backend = ""
			case "sqlite":
				cfg.State.Backend = "sqlite://" + filepath.Join(t.TempDir(), "api.db")
			case "postgres":
				cfg.State.Backend = isolatedPostgresTestDSN(t)
			}
			first := NewServerWithConfig(cfg)
			registerDockerAgent(t, first)
			rev := publishRevision(t, first, upsertApplyHCL)
			response := keyedApply(t, first, rev, "recover-inputs", "secret-recovery-canary")
			require.Equal(t, 202, response.Code, response.Body.String())
			var accepted struct {
				RunID string `json:"run_id"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &accepted))
			ctx := context.Background()
			run, err := first.apiStore.GetRun(ctx, accepted.RunID)
			require.NoError(t, err)
			run, claimed, err := first.apiStore.ClaimRun(ctx, run.ID, "host-a", "worker", time.Millisecond)
			require.NoError(t, err)
			require.True(t, claimed)
			time.Sleep(2 * time.Millisecond)
			run, expired, err := first.apiStore.ExpireRunLease(ctx, run.ID, time.Now().UTC())
			require.NoError(t, err)
			require.True(t, expired)

			cold := NewServerWithConfig(cfg)
			query := httptest.NewRecorder()
			cold.ServeHTTP(query, httptest.NewRequest("GET", "/v1/runs/"+run.ID, nil))
			require.Contains(t, query.Body.String(), `"inputs_required":true`)
			_, err = cold.recoveryAuthorization(ctx, *run, controlplane.RunRecoveryRecover)
			require.Error(t, err)
			require.Equal(t, 409, keyedApply(t, cold, rev, "recover-inputs", "changed-secret").Code)
			response = keyedApply(t, cold, rev, "recover-inputs", "secret-recovery-canary")
			require.Equal(t, 202, response.Code, response.Body.String())
			auth, err := cold.recoveryAuthorization(ctx, *run, controlplane.RunRecoveryRecover)
			require.NoError(t, err)
			wire, err := json.Marshal(auth)
			require.NoError(t, err)
			require.Contains(t, string(wire), "secret-recovery-canary")
			require.Empty(t, auth.Run.Inputs, "input handoff must not be embedded in the durable Run")

			owner, err := cold.apiStore.GetAgent(ctx, "host-a")
			require.NoError(t, err)
			owner.Capabilities = append(owner.Capabilities, controlplane.RunRecoveryCapability)
			require.NoError(t, cold.apiStore.SaveAgent(ctx, *owner))
			response = remoteRecoveryRequest(t, cold, "POST", "/v1/runs/"+run.ID+"/remote/recover", nil)
			require.Equal(t, 409, response.Code, "legacy agent must not ignore recovery input values")
			owner.Capabilities = append(owner.Capabilities, controlplane.RunRecoveryInputsCapability)
			require.NoError(t, cold.apiStore.SaveAgent(ctx, *owner))
			response = remoteRecoveryRequest(t, cold, "POST", "/v1/runs/"+run.ID+"/remote/recover", nil)
			require.Equal(t, 202, response.Code, response.Body.String())
			var command map[string]string
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &command))
			lease, ok, err := cold.apiStore.AcquireAgentCommandLease(ctx, "host-a", command["command_id"], "recovery-worker", time.Minute)
			require.NoError(t, err)
			require.True(t, ok)
			delivery := map[string]any{"lease_owner": lease.LeaseOwner, "attempt": lease.Attempt}
			authorizePath := "/v1/agents/host-a/commands/" + lease.ID + "/recovery-authorize"
			response = remoteRecoveryRequest(t, cold, "POST", authorizePath, delivery)
			require.Equal(t, 409, response.Code, "legacy host must not receive inputs it cannot consume")
			delivery["inputs_protocol"] = 1
			response = remoteRecoveryRequest(t, cold, "POST", authorizePath, delivery)
			require.Equal(t, 200, response.Code, response.Body.String())
			var wireAuth controlplane.RunRecoveryAuthorization
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &wireAuth))
			require.Equal(t, "secret-recovery-canary", wireAuth.Inputs["flag"])
			require.Empty(t, wireAuth.Run.Inputs)
			stored, err := cold.apiStore.GetRun(ctx, run.ID)
			require.NoError(t, err)
			require.Empty(t, stored.Inputs)
			commands, err := cold.apiStore.ListAgentCommands(ctx, "host-a")
			require.NoError(t, err)
			raw, err := json.Marshal(commands)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "secret-recovery-canary")
			otherAPI := NewServerWithConfig(cfg)
			_, err = otherAPI.recoveryAuthorization(ctx, *run, controlplane.RunRecoveryRecover)
			require.Error(t, err, "failover without resubmission must stay blocked")
			changed := *run
			changed.QueuedAt = changed.QueuedAt.Add(time.Second)
			_, err = cold.recoveryAuthorization(ctx, changed, controlplane.RunRecoveryRecover)
			require.Error(t, err, "inputs cannot cross incarnation")
		})
	}
}
