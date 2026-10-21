package effectgate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/controlplane"
)

// FenceAdoption preserves old journal bytes before installing an explicit unknown,
// stopped outcome. It accepts no success inference from resource absence.
// Canonical authority must authorize the admitted fence before this call.
func (g *Gate) FenceAdoption(ctx context.Context, p controlplane.AdoptionDispositionProof, key ed25519.PublicKey, authorize func(context.Context) error) error {
	if err := controlplane.VerifyAdoptionDisposition(key, p); err != nil {
		return err
	}
	if g.BootID != p.HostBootID || g.Root == "" {
		return ErrFenced
	}
	topology := p.Adoption.TopologyUID
	if err := os.MkdirAll(g.Root, 0700); err != nil {
		return err
	}
	name := fmt.Sprintf("%x", sha256.Sum256([]byte(topology)))
	lock := flock.New(filepath.Join(g.Root, name+".lock"))
	held, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil || !held {
		lock.Close()
		if err != nil {
			return err
		}
		return ctx.Err()
	}
	defer lock.Close()
	if authorize == nil {
		return ErrFenced
	}
	if err = authorize(ctx); err != nil {
		return err
	}
	path := filepath.Join(g.Root, name+".json")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	record := &ledger{}
	valid := err == nil && json.Unmarshal(raw, record) == nil && record.Version == 2 && record.TopologyUID == topology && record.Epochs != nil && record.Outcomes != nil && record.Stopped != nil && validateLedger(record) == nil
	if valid {
		for _, prior := range record.Outcomes {
			if prior.State == "fenced" && sameGrant(prior.Grant, p.Request.Grant) {
				if prior.AdoptionFence == nil || !sameAdoptionDisposition(*prior.AdoptionFence, p) {
					return ErrUncertain
				}
				return nil
			}
			if !record.Stopped[prior.Grant.Epoch] && (prior.Grant.TargetBootID != p.Request.Grant.TargetBootID || prior.Grant.AgentID != p.Request.Grant.AgentID) {
				return ErrUncertain
			}
		}
	}
	// Archive is content-addressed; repeating recovery cannot overwrite another
	// historical journal. Missing bytes are recorded explicitly as a null payload.
	archive, _ := json.Marshal(struct {
		Proof   controlplane.AdoptionDispositionProof `json:"proof"`
		Journal []byte                                `json:"journal"`
	}{p, raw})
	archivePath := filepath.Join(g.Root, fmt.Sprintf("adoption-fenced-%x.evidence", sha256.Sum256(archive)))
	if existing, readErr := os.ReadFile(archivePath); readErr == nil {
		if string(existing) != string(archive) {
			return fmt.Errorf("%w: existing fence archive corrupt", ErrUncertain)
		}
	} else if !os.IsNotExist(readErr) {
		return readErr
	} else {
		f, err := os.CreateTemp(g.Root, ".adoption-fence-evidence-")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		defer f.Close()
		if _, err = f.Write(archive); err != nil {
			return err
		}
		if err = f.Sync(); err != nil {
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		if err = os.Rename(f.Name(), archivePath); err != nil {
			return err
		}
	}
	dir, err := os.Open(g.Root)
	if err != nil {
		return err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return err
	}
	if !valid {
		record = &ledger{Version: 2, TopologyUID: topology, Epochs: map[string]uint64{}, Outcomes: map[string]outcome{}, Stopped: map[uint64]bool{}}
	}
	grant := p.Request.Grant
	record.Outcomes[outcomeKey(grant, p.Adoption.ExternalID)] = outcome{Grant: grant, State: "fenced", AdoptionFence: &p, AdoptionFenceKey: append([]byte(nil), key...)}
	record.Stopped[grant.Epoch] = true
	if record.Epochs[grant.Scope.ResourceUID] < grant.Epoch {
		record.Epochs[grant.Scope.ResourceUID] = grant.Epoch
	}
	return save(path, record)
}
func sameAdoptionDisposition(a, b controlplane.AdoptionDispositionProof) bool {
	ar, _ := json.Marshal(a)
	br, _ := json.Marshal(b)
	return string(ar) == string(br)
}

func (g *Gate) ReadAdoptionDisposition(ctx context.Context, request controlplane.HostFenceRequest) (controlplane.AdoptionDispositionProof, error) {
	_, record, release, err := g.locked(ctx, request.Grant.Scope.TopologyUID)
	if err != nil {
		return controlplane.AdoptionDispositionProof{}, err
	}
	defer release()
	for _, prior := range record.Outcomes {
		if prior.State == "fenced" && prior.AdoptionFence != nil && sameGrant(prior.Grant, request.Grant) {
			a, _ := request.Digest()
			b, _ := prior.AdoptionFence.Request.Digest()
			if a != b {
				return controlplane.AdoptionDispositionProof{}, ErrFenced
			}
			return *prior.AdoptionFence, nil
		}
	}
	return controlplane.AdoptionDispositionProof{}, ErrNotFound
}
