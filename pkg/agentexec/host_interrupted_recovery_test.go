package agentexec

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

type coldContinuationBridge struct {
	*LocalBridge
	runs     map[string]*controlplane.Run
	verified []string
	deny     string
}

func (b *coldContinuationBridge) ParentRun(_ context.Context, id string) (*controlplane.Run, error) {
	r := b.runs[id]
	if r == nil {
		return nil, fmt.Errorf("missing canonical Run")
	}
	return r, nil
}
func (b *coldContinuationBridge) MaterializeRunRevision(context.Context, string, string) (string, error) {
	return "", fmt.Errorf("recovery must not execute")
}
func (b *coldContinuationBridge) VerifyResetContinuation(_ context.Context, child, parent *controlplane.Run) error {
	if parent.ID == b.deny {
		return fmt.Errorf("missing signed predecessor stop")
	}
	b.verified = append(b.verified, child.ID+"->"+parent.ID)
	return nil
}

func TestColdInterruptedResetContinuationChecksEveryPredecessor(t *testing.T) {
	for _, deny := range []string{"", "first", "root"} {
		t.Run("deny-"+deny, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			require.NoError(t, os.Chmod(root, 0700))
			b := &coldContinuationBridge{LocalBridge: NewLocalBridge(LocalOptions{Topology: "lab", StatePath: filepath.Join(root, "state.json"), RunsDir: root, Log: io.Discard}), runs: map[string]*controlplane.Run{}, deny: deny}
			now := time.Now().UTC()
			origin := controlplane.Run{ID: "root", Topology: "lab", Op: "reset", Revision: "revision", AgentID: "agent", Status: controlplane.RunFailed, QueuedAt: now, EndedAt: now, Attempt: 1, LeaseOwner: "owner"}
			first := origin
			first.ID, first.ParentID = "first", "root"
			current := first
			current.ID, current.ParentID, current.Status = "current", "first", controlplane.RunRunning
			b.runs[origin.ID], b.runs[first.ID], b.runs[current.ID] = &origin, &first, &current
			mgr, err := b.StateManager("lab")
			require.NoError(t, err)
			st, err := mgr.Load()
			require.NoError(t, err)
			node := state.Resource{Address: address.Resource("sysbox_node", "web"), OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1}
			st.Resources = append(st.Resources, node)
			require.NoError(t, mgr.Save(st))
			repo, err := b.ActionRepository("lab")
			require.NoError(t, err)
			action := runtime.ActionRecord{ID: "original-action", Topology: "lab", RunID: "root", RunQueuedAt: now, RunRevision: "revision", Resource: node.Address, OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 2, Candidate: &node, StateSerial: st.Meta.Serial, ExpectedSerial: st.Meta.Serial, Action: model.PlanActionReset, Status: runtime.ActionIntentCommitted, Phase: "accepted"}
			require.NoError(t, repo.CommitActionIntent(ctx, action))
			pub, _, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			_, key, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			tasksRoot := filepath.Join(root, "tasks")
			require.NoError(t, os.Mkdir(tasksRoot, 0700))
			h := &HostRunExecutor{Bridge: b, Tasks: &hostsupervisor.Dispatcher{Root: tasksRoot, AgentID: "agent", IssuerKey: pub, HostKey: key}}
			raw, err := h.RecoverInterruptedRun(ctx, hostsupervisor.RunRequest{RunID: current.ID, Topology: "lab", TopologyUID: st.TopologyUID, Revision: "revision", Action: "reset"}, current)
			if deny != "" {
				require.Error(t, err)
				require.Empty(t, raw)
				return
			}
			require.NoError(t, err)
			var result controlplane.Run
			require.NoError(t, json.Unmarshal(raw, &result))
			require.Equal(t, current.ID, result.ID)
			require.Equal(t, first.ID, result.ParentID)
			require.True(t, result.Recoverable)
			require.Equal(t, []string{"current->first", "first->root"}, b.verified)
			actions, err := repo.LoadActions(ctx, "lab")
			require.NoError(t, err)
			require.Len(t, actions, 1)
			require.Equal(t, "root", actions[0].RunID)
		})
	}
}
