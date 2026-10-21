package agentexec

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/revisiongate"
	"os"
	"path/filepath"
	"regexp"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
)

func (b *LocalBridge) VerifyResetContinuation(ctx context.Context, run, parent *controlplane.Run) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if run == nil || parent == nil || run.ParentID != parent.ID || run.Topology != parent.Topology {
		return fmt.Errorf("reset continuation parent mismatch")
	}
	proof, err := b.loadResetReturn(parent.ID)
	if err != nil {
		return err
	}
	if !proof.QueuedAt.Equal(parent.QueuedAt) || proof.Revision != parent.Revision {
		return fmt.Errorf("reset continuation incarnation changed")
	}
	return nil
}

var localRunID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// A return receipt proves the synchronous executor returned. It does not prove
// an uncertain provider request completed; continuation still rejects unknown
// action phases. Old checkpoints alone cannot manufacture this receipt.
type localResetReturn struct {
	Version          int              `json:"version"`
	Run              controlplane.Run `json:"run"`
	CheckpointSHA256 string           `json:"checkpoint_sha256"`
}

func (b *LocalBridge) recordResetReturn(run *controlplane.Run) error {
	if !localRunID.MatchString(run.ID) || run.Topology != b.opts.Topology {
		return fmt.Errorf("invalid local reset identity")
	}
	path := b.CheckpointFile(run.Topology, run.ID)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	} // no reset journal/effects were started
	if err != nil {
		return err
	}
	var cp runtime.OperationCheckpoint
	if json.Unmarshal(raw, &cp) != nil || cp.RunID != run.ID || cp.Topology != run.Topology || cp.Operation != "reset" || cp.EndedAt.IsZero() {
		return fmt.Errorf("reset checkpoint has not returned")
	}
	copy := *run
	copy.Inputs = nil
	receipt := localResetReturn{Version: 1, Run: copy, CheckpointSHA256: fmt.Sprintf("%x", sha256.Sum256(raw))}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	release, err := revisiongate.Guard(context.Background(), path+".returned", encoded)
	if err != nil {
		return err
	}
	defer release()
	f, err := os.CreateTemp(filepath.Dir(path), ".reset-return-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(encoded); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Link(f.Name(), path+".returned"); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (b *LocalBridge) loadResetReturn(id string) (*controlplane.Run, error) {
	if !localRunID.MatchString(id) {
		return nil, fmt.Errorf("invalid parent reset Run ID")
	}
	path := b.CheckpointFile(b.opts.Topology, id)
	raw, err := os.ReadFile(path + ".returned")
	if err != nil {
		return nil, fmt.Errorf("original reset executor return evidence unavailable: %w", err)
	}
	var proof localResetReturn
	if json.Unmarshal(raw, &proof) != nil || proof.Version != 1 || proof.Run.ID != id || proof.Run.Topology != b.opts.Topology || proof.Run.Op != "reset" || !proof.Run.Status.IsTerminal() || proof.Run.EndedAt.IsZero() || proof.Run.QueuedAt.IsZero() {
		return nil, fmt.Errorf("invalid original reset return evidence")
	}
	checkpoint, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if proof.CheckpointSHA256 != fmt.Sprintf("%x", sha256.Sum256(checkpoint)) {
		return nil, fmt.Errorf("original reset checkpoint changed after return")
	}
	return &proof.Run, nil
}
