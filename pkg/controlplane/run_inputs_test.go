package controlplane

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRunInputBindingPreservesKeyedRequestAndRejectsChanges(t *testing.T) {
	inputs := map[string]string{"flag": "secret-canary", "size": "2"}
	raw, _ := json.Marshal(struct {
		Topology, Revision, Agent string
		Unsafe                    bool
		Inputs                    map[string]string
		Deadline                  time.Time
	}{"lab", "rev", "", false, inputs, time.Time{}})
	fingerprint := fmt.Sprintf("apply-request:%x", sha256.Sum256(raw))
	run := Run{Topology: "lab", Revision: "rev", Op: "apply", AgentID: "selected", RequestFingerprint: fingerprint}
	require.NoError(t, ValidateRunInputs(run, inputs))
	require.Error(t, ValidateRunInputs(run, map[string]string{"size": "3"}))
	require.Error(t, ValidateRunInputs(run, nil))
	require.Equal(t, fingerprint, RunRequestFingerprint("apply", "lab", "rev", "", "", false, inputs, time.Time{}))
	run.Target = "sysbox_node.web"
	require.Error(t, ValidateRunInputs(run, inputs))
	run.RequestFingerprint = RunRequestFingerprint("apply", "lab", "rev", "", "sysbox_node.web", false, inputs, time.Time{})
	require.NoError(t, ValidateRunInputs(run, inputs))
}
