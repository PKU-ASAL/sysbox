package effectgate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/revisiongate"
)

type returnedOutcome struct {
	Grant    controlplane.SignedExecutionGrant `json:"grant"`
	EffectID string                            `json:"effect_id"`
	Result   json.RawMessage                   `json:"result"`
}

func (g *Gate) returnedPath(grant controlplane.ExecutionGrant, id string) string {
	raw, _ := json.Marshal(struct {
		Grant controlplane.ExecutionGrant
		ID    string
	}{grant, id})
	return filepath.Join(g.Root, fmt.Sprintf("returned-%x.proof.json", sha256.Sum256(raw)))
}

// Only provider callbacks which return complete exact-identity evidence use this
// boundary. Cancellation/error never writes a return proof.
func (g *Gate) ExecuteWithReturnProof(ctx context.Context, signed controlplane.SignedExecutionGrant, id string, effect func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	if signed.Grant.Operation != "adoption_apply" {
		return nil, ErrFenced
	}
	return g.execute(ctx, signed, id, effect, true)
}
func (g *Gate) saveReturned(signed controlplane.SignedExecutionGrant, id string, result json.RawMessage) error {
	raw, err := json.Marshal(returnedOutcome{Grant: signed, EffectID: id, Result: result})
	if err != nil {
		return err
	}
	if len(raw) > 16<<20 {
		return fmt.Errorf("returned outcome exceeds proof budget")
	}
	path := g.returnedPath(signed.Grant, id)
	if info, e := os.Lstat(path); e == nil && (!info.Mode().IsRegular() || info.Size() > 16<<20) {
		return ErrUncertain
	} else if e != nil && !os.IsNotExist(e) {
		return e
	}
	if prior, err := os.ReadFile(path); err == nil {
		if string(prior) != string(raw) {
			return ErrUncertain
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	unlock, err := revisiongate.Guard(context.Background(), path, raw)
	if err != nil {
		return err
	}
	defer unlock()
	f, err := os.CreateTemp(g.Root, ".returned-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(g.Root)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (g *Gate) RecoverReturnedOutcome(ctx context.Context, grant controlplane.ExecutionGrant, id string) (json.RawMessage, error) {
	if grant.Operation != "adoption_apply" || grant.TargetBootID != g.BootID {
		return nil, ErrFenced
	}
	path, record, release, err := g.locked(ctx, grant.Scope.TopologyUID)
	if err != nil {
		return nil, err
	}
	defer release()
	key := outcomeKey(grant, id)
	prior, ok := record.Outcomes[key]
	if !ok || !sameGrant(prior.Grant, grant) {
		return nil, ErrUncertain
	}
	if prior.State == "complete" {
		return append(json.RawMessage(nil), prior.Result...), nil
	}
	if prior.State != "intent" || record.Stopped[grant.Epoch] {
		return nil, ErrUncertain
	}
	proofPath := g.returnedPath(grant, id)
	info, err := os.Lstat(proofPath)
	if err != nil {
		return nil, ErrUncertain
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, ErrUncertain
	}
	raw, err := os.ReadFile(proofPath)
	if err != nil {
		return nil, err
	}
	var proof returnedOutcome
	if json.Unmarshal(raw, &proof) != nil || proof.EffectID != id || !sameGrant(proof.Grant.Grant, grant) || len(proof.Result) == 0 || !json.Valid(proof.Result) || controlplane.VerifyExecutionGrant(g.PublicKey, proof.Grant, g.BootID, proof.Grant.IssuedAt) != nil {
		return nil, ErrUncertain
	}
	prior.State = "complete"
	prior.Result = proof.Result
	record.Outcomes[key] = prior
	if err = save(path, record); err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), proof.Result...), nil
}
func (g *Gate) RecoverReturned(ctx context.Context) error {
	return g.recoverOutcomes(ctx, 0, func(prior outcome) bool { return prior.State == "intent" && prior.Grant.Operation == "adoption_apply" }, func(ctx context.Context, grant controlplane.ExecutionGrant, id string) error {
		if _, err := os.Lstat(g.returnedPath(grant, id)); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
		_, err := g.RecoverReturnedOutcome(ctx, grant, id)
		return err
	})
}
