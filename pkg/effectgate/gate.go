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
	"os"
	"path/filepath"
	"time"
)

var ErrUncertain = errors.New("host effect outcome uncertain; observation required")
var ErrFenced = errors.New("host effect authorization rejected")

type Gate struct {
	Root      string
	PublicKey ed25519.PublicKey
	BootID    string
	Authorize func(context.Context, controlplane.ExecutionGrant) error
}
type outcome struct {
	Grant  controlplane.ExecutionGrant `json:"grant"`
	State  string                      `json:"state"`
	Result json.RawMessage             `json:"result,omitempty"`
}
type ledger struct {
	Version  int                `json:"version"`
	Epochs   map[string]uint64  `json:"epochs"`
	Outcomes map[string]outcome `json:"outcomes"`
	Stopped  map[uint64]bool    `json:"stopped"`
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
	value := &ledger{Version: 1, Epochs: map[string]uint64{}, Outcomes: map[string]outcome{}, Stopped: map[uint64]bool{}}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		release()
		return "", nil, nil, err
	}
	if err == nil {
		if json.Unmarshal(raw, value) != nil || value.Version != 1 || value.Epochs == nil || value.Outcomes == nil || value.Stopped == nil {
			release()
			return "", nil, nil, ErrUncertain
		}
	}
	return path, value, release, nil
}
func save(path string, value *ledger) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
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
		if prior.Grant.ActionID != grant.ActionID || prior.Grant.CommandID != grant.CommandID || prior.Grant.Digest != grant.Digest || prior.Grant.Operation != grant.Operation {
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
		if prior.State != "complete" {
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
	record.Outcomes[key] = outcome{Grant: grant, State: "complete", Result: result}
	if err := save(path, record); err != nil {
		return nil, err
	}
	return result, nil
}

// Stop is only exposed to the authenticated revocation supervisor. Waiting for
// the topology lock waits for callbacks to return; unknown intent cannot yield
// a stop receipt and prevents takeover until provider observation resolves it.
func (g *Gate) Stop(ctx context.Context, grant controlplane.ExecutionGrant) (string, error) {
	path, record, release, err := g.locked(ctx, grant.Scope.TopologyUID)
	if err != nil {
		return "", err
	}
	defer release()
	for _, prior := range record.Outcomes {
		if prior.Grant.Epoch == grant.Epoch && prior.State != "complete" {
			return "", ErrUncertain
		}
	}
	record.Stopped[grant.Epoch] = true
	if grant.Epoch > record.Epochs[grant.Scope.ResourceUID] {
		record.Epochs[grant.Scope.ResourceUID] = grant.Epoch
	}
	if err := save(path, record); err != nil {
		return "", err
	}
	raw, _ := json.Marshal(grant)
	return fmt.Sprintf("%x", sha256.Sum256(append([]byte(g.BootID+"\x00stopped\x00"), raw...))), nil
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
