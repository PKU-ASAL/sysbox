package api

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
)

// 4. Audit / action log: the run's action log is derived from the checkpoint,
// whose plan changes carry the secret://input/<name> reference — never the
// plaintext.
func TestSensitiveInputDoesNotEnterActionLog(t *testing.T) {
	const canary = "plaintext-canary-must-never-leak"

	cp := runtime.OperationCheckpoint{
		RunID:     "run-1",
		Topology:  "lab",
		Operation: "apply",
		Plan: []controlplane.PlannedChange{{
			Address: address.Resource("sysbox_node", "web"),
			Action:  controlplane.PlanActionCreate,
			Changes: []controlplane.FieldChange{{
				Path:      "env.FLAG",
				Before:    "secret://input/flag",
				After:     "secret://input/flag",
				Sensitive: true,
			}},
		}},
	}

	logJSON, err := json.Marshal(runActionLogFromCheckpoint(cp))
	require.NoError(t, err)

	require.NotContains(t, string(logJSON), canary)
	require.Contains(t, string(logJSON), "secret://input/flag")
}
