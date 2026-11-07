package controlplane

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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

func TestResetContinuationBindingIncludesParentIncarnation(t *testing.T) {
	values := map[string]string{"password": "private"}
	parent := Run{ID: "parent", Op: "reset", Topology: "lab", Revision: "rev", AgentID: "host", QueuedAt: time.Now()}
	child := Run{ID: "child", ParentID: parent.ID, Op: "reset", Topology: parent.Topology, Revision: parent.Revision, AgentID: parent.AgentID}
	child.RequestFingerprint = ResetContinuationFingerprint(parent, child, values)
	require.NoError(t, ValidateRunInputs(child, values))
	require.Error(t, ValidateRunInputs(child, map[string]string{"password": "different"}))
	changed := parent
	changed.QueuedAt = parent.QueuedAt.Add(time.Second)
	require.NotEqual(t, child.RequestFingerprint, ResetContinuationFingerprint(changed, child, values))
	child.ParentID = ""
	require.Error(t, ValidateRunInputs(child, values))
}
