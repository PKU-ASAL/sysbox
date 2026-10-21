// Package effectgate is the resource host's durable effect boundary. Provider
// credentials belong to its serving process, never to clients submitting grants.
package effectgate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/revisiongate"
	"os"
	"path/filepath"
	"time"
)

var ErrUncertain = errors.New("host effect outcome uncertain; observation required")
var ErrFenced = errors.New("host effect authorization rejected")
var ErrNotFound = errors.New("host effect outcome not found")

type Gate struct {
	Root      string
	PublicKey ed25519.PublicKey
	BootID    string
	Authorize func(context.Context, controlplane.ExecutionGrant) error
}
type outcome struct {
	GCFence     *controlplane.GCDispositionProof         `json:"gc_fence,omitempty"`
	GCFenceKey  []byte                                   `json:"gc_fence_key,omitempty"`
	Grant       controlplane.ExecutionGrant              `json:"grant"`
	SignedGrant *controlplane.SignedExecutionGrant       `json:"signed_grant,omitempty"`
	State       string                                   `json:"state"`
	Result      json.RawMessage                          `json:"result,omitempty"`
	Checkpoint  json.RawMessage                          `json:"run_checkpoint,omitempty"`
	Receipt     *controlplane.SignedExecutionStopReceipt `json:"receipt,omitempty"`
	Reported    bool                                     `json:"reported"`
	Started     bool                                     `json:"started,omitempty"`
	Admission   json.RawMessage                          `json:"run_admission,omitempty"`
}
type ledger struct {
	TopologyUID string             `json:"topology_uid"`
	Version     int                `json:"version"`
	Epochs      map[string]uint64  `json:"epochs"`
	Outcomes    map[string]outcome `json:"outcomes"`
	Stopped     map[uint64]bool    `json:"stopped"`
}

func (g *Gate) locked(ctx context.Context, topology string) (string, *ledger, func(), error) {
	if g.Root == "" || g.BootID == "" || topology == "" {
		return "", nil, nil, ErrFenced
	}
	if err := os.MkdirAll(g.Root, 0700); err != nil {
		return "", nil, nil, err
	}
	name := fmt.Sprintf("%x", sha256.Sum256([]byte(topology)))
	lock := flock.New(filepath.Join(g.Root, name+".lock"))
	held, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil || !held {
		lock.Close()
		if err == nil {
			err = ctx.Err()
		}
		return "", nil, nil, err
	}
	release := func() { lock.Close() }
	path := filepath.Join(g.Root, name+".json")
	value := &ledger{Version: 2, TopologyUID: topology, Epochs: map[string]uint64{}, Outcomes: map[string]outcome{}, Stopped: map[uint64]bool{}}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		release()
		return "", nil, nil, err
	}
	if err == nil {
		if json.Unmarshal(raw, value) != nil || value.Version != 2 || value.TopologyUID != topology || value.Epochs == nil || value.Outcomes == nil || value.Stopped == nil {
			release()
			return "", nil, nil, fmt.Errorf("%w: effect ledger requires explicit rebuild", ErrUncertain)
		}
		if err := validateLedger(value); err != nil {
			release()
			return "", nil, nil, err
		}
	}
	return path, value, release, nil
}
func save(path string, value *ledger) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	release, err := revisiongate.Guard(context.Background(), path, raw)
	if err != nil {
		return err
	}
	defer release()
	file, err := os.CreateTemp(filepath.Dir(path), ".effect-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(raw); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Execute must run in the gateway process. The callback cannot be supplied by
// an untrusted remote client; its operation and digest are selected by a fixed
// provider dispatcher. The topology lock spans authorization and the effect.
func (g *Gate) Execute(ctx context.Context, signed controlplane.SignedExecutionGrant, effectID string, effect func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	return g.execute(ctx, signed, effectID, effect, false)
}
func (g *Gate) execute(ctx context.Context, signed controlplane.SignedExecutionGrant, effectID string, effect func(context.Context) (json.RawMessage, error), returnProof bool) (json.RawMessage, error) {
	if effectID == "" || effect == nil || g.Authorize == nil {
		return nil, ErrFenced
	}
	if err := controlplane.VerifyExecutionGrant(g.PublicKey, signed, g.BootID, time.Now()); err != nil {
		return nil, err
	}
	path, record, release, err := g.locked(ctx, signed.Grant.Scope.TopologyUID)
	if err != nil {
		return nil, err
	}
	defer release()
	grant := signed.Grant
	if grant.Epoch < record.epochFloor(grant.Scope.ResourceUID) || record.Stopped[grant.Epoch] {
		return nil, ErrFenced
	}
	key := fmt.Sprintf("%d/%s", grant.Epoch, effectID)
	if prior, ok := record.Outcomes[key]; ok {
		if !sameGrant(prior.Grant, grant) {
			return nil, ErrFenced
		}
		if prior.State != "complete" {
			return nil, ErrUncertain
		}
		if err := g.Authorize(ctx, grant); err != nil {
			return nil, err
		}
		return append(json.RawMessage(nil), prior.Result...), nil
	}
	for _, prior := range record.Outcomes {
		if prior.State != "complete" && prior.State != "fenced" {
			return nil, ErrUncertain
		}
	}
	if err := g.Authorize(ctx, grant); err != nil {
		return nil, err
	}
	budget, err := controlplane.ExecutionGrantBudget(signed, time.Now())
	if err != nil {
		return nil, err
	}
	effectCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	record.Epochs[grant.Scope.ResourceUID] = grant.Epoch
	record.Outcomes[key] = outcome{Grant: grant, State: "intent"}
	if err := save(path, record); err != nil {
		return nil, err
	}
	result, err := effect(effectCtx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	if effectCtx.Err() != nil {
		return nil, fmt.Errorf("%w: %v", ErrUncertain, effectCtx.Err())
	}
	if len(result) == 0 {
		result = json.RawMessage(`null`)
	}
	if !json.Valid(result) {
		return nil, ErrUncertain
	}
	if returnProof {
		if err := g.saveReturned(signed, effectID, result); err != nil {
			return nil, err
		}
	}
	record.Outcomes[key] = outcome{Grant: grant, State: "complete", Result: result}
	if err := save(path, record); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *ledger) epochFloor(resource string) uint64 {
	floor := r.Epochs[""]
	if resource != "" {
		if r.Epochs[resource] > floor {
			floor = r.Epochs[resource]
		}
		return floor
	}
	for _, epoch := range r.Epochs {
		if epoch > floor {
			floor = epoch
		}
	}
	return floor
}
