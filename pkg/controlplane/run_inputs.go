package controlplane

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// RunRequestFingerprint binds transient values without persisting plaintext.
// Keep the empty-target encoding stable for existing immutable request keys.
func RunRequestFingerprint(operation, topology, revision, agent, target string, unsafe bool, inputs map[string]string, deadline time.Time) string {
	if len(inputs) == 0 {
		inputs = nil
	}
	raw, _ := json.Marshal(struct {
		Topology, Revision, Agent string
		Unsafe                    bool
		Inputs                    map[string]string
		Deadline                  time.Time
		Target                    string `json:"Target,omitempty"`
	}{topology, revision, agent, unsafe, inputs, deadline.UTC(), target})
	return fmt.Sprintf("%s-request:%x", operation, sha256.Sum256(raw))
}

func ApplyOperationKey(revision string, inputs map[string]string, unsafe bool) string {
	keys := make([]string, 0, len(inputs))
	for key := range inputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "revision:%d:%s\n", len(revision), revision)
	fmt.Fprintf(&b, "allow_unsafe_state:%t\n", unsafe)
	for _, key := range keys {
		value := inputs[key]
		fmt.Fprintf(&b, "input:%d:%s=%d:%s\n", len(key), key, len(value), value)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(b.String())))
}

// ValidateRunInputs checks the exact durable request. Automatic placement and
// explicit placement can only have selected this canonical owning Agent.
func ValidateRunInputs(run Run, inputs map[string]string) error {
	if run.Op == "apply" && strings.HasPrefix(run.RequestFingerprint, "apply-request:") {
		for _, agent := range []string{"", run.AgentID} {
			if RunRequestFingerprint(run.Op, run.Topology, run.Revision, agent, run.Target, run.UnsafeState, inputs, run.DeadlineAt) == run.RequestFingerprint {
				return nil
			}
		}
		return fmt.Errorf("host Run inputs do not match the admitted request")
	}
	if run.Op == "apply" && len(run.OperationKey) == sha256.Size*2 {
		if ApplyOperationKey(run.Revision, inputs, run.UnsafeState) == run.OperationKey {
			return nil
		}
		return fmt.Errorf("host Run inputs do not match the admitted operation")
	}
	if len(inputs) > 0 {
		return fmt.Errorf("Run has no authenticated input binding")
	}
	return nil
}
