package agentexec

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type resetAncestryBridge struct {
	Bridge
	runs     map[string]*controlplane.Run
	verified []string
	deny     string
}

func (b *resetAncestryBridge) ParentRun(_ context.Context, id string) (*controlplane.Run, error) {
	if r := b.runs[id]; r != nil {
		return r, nil
	}
	return nil, fmt.Errorf("missing parent")
}
func (b *resetAncestryBridge) VerifyResetContinuation(_ context.Context, child, parent *controlplane.Run) error {
	if parent.ID == b.deny {
		return fmt.Errorf("missing stop proof")
	}
	b.verified = append(b.verified, child.ID+"->"+parent.ID)
	return nil
}
func TestResetOriginVerifiesEveryAncestor(t *testing.T) {
	root := &controlplane.Run{ID: "root", Topology: "lab", Op: "reset", Revision: "rev", Status: controlplane.RunFailed, QueuedAt: time.Now()}
	first := *root
	first.ID, first.ParentID = "first", "root"
	child := first
	child.ID, child.ParentID, child.Status = "second", "first", controlplane.RunRunning
	for _, scenario := range []string{"valid", "missing-stop", "cycle", "changed-revision", "running-parent"} {
		t.Run(scenario, func(t *testing.T) {
			r, f := *root, first
			b := &resetAncestryBridge{runs: map[string]*controlplane.Run{"root": &r, "first": &f}}
			switch scenario {
			case "missing-stop":
				b.deny = "root"
			case "cycle":
				r.ParentID = "first"
			case "changed-revision":
				r.Revision = "other"
			case "running-parent":
				r.Status = controlplane.RunRunning
			}
			origin, err := ResolveResetOrigin(context.Background(), b, &child)
			if scenario == "valid" {
				require.NoError(t, err)
				require.Equal(t, "root", origin.ID)
				require.Equal(t, []string{"second->first", "first->root"}, b.verified)
			} else {
				require.Error(t, err)
				require.Nil(t, origin)
			}
		})
	}
}
