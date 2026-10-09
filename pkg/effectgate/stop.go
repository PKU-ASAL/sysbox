package effectgate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func sameGrant(a, b controlplane.ExecutionGrant) bool {
	a.ExpiresAt = a.ExpiresAt.UTC().Round(0)
	b.ExpiresAt = b.ExpiresAt.UTC().Round(0)
	return a == b
}
func outcomeKey(grant controlplane.ExecutionGrant, effectID string) string {
	return fmt.Sprintf("%d/%s", grant.Epoch, effectID)
}
func receiptMatches(receipt *controlplane.SignedExecutionStopReceipt, grant controlplane.ExecutionGrant) bool {
	return receipt != nil && sameGrant(receipt.Grant, grant) && receipt.HostBootID != "" && receipt.ReceiptID != "" && !receipt.StoppedAt.IsZero() && receipt.Quiescence == "confirmed" && len(receipt.Signature) == ed25519.SignatureSize
}
func validateLedger(record *ledger) error {
	for key, prior := range record.Outcomes {
		if len(prior.Checkpoint) != 0 && (prior.SignedGrant == nil || !json.Valid(prior.Checkpoint)) {
			return ErrUncertain
		}
		if prior.SignedGrant != nil && (!sameGrant(prior.Grant, prior.SignedGrant.Grant) || prior.SignedGrant.BootID == "" || prior.SignedGrant.IssuedAt.IsZero() || len(prior.SignedGrant.Signature) != ed25519.SignatureSize || !isRunGrant(prior.Grant)) {
			return ErrUncertain
		}
		prefix := fmt.Sprintf("%d/", prior.Grant.Epoch)
		if prior.Grant.Scope.TopologyUID != record.TopologyUID || prior.Grant.Epoch == 0 || !strings.HasPrefix(key, prefix) || len(key) == len(prefix) {
			return ErrUncertain
		}
		if prior.State != "intent" && prior.State != "complete" {
			return ErrUncertain
		}
		if prior.State == "complete" && (len(prior.Result) == 0 || !json.Valid(prior.Result)) {
			return ErrUncertain
		}
		if prior.Reported && prior.Receipt == nil {
			return ErrUncertain
		}
		if prior.Receipt != nil && (prior.State != "complete" || !record.Stopped[prior.Grant.Epoch] || !receiptMatches(prior.Receipt, prior.Grant)) {
			return ErrUncertain
		}
	}
	return nil
}
func completed(record *ledger, grant controlplane.ExecutionGrant, effectID string) (outcome, error) {
	if effectID == "" || grant.Scope.TopologyUID == "" || grant.Epoch == 0 {
		return outcome{}, ErrFenced
	}
	prior, ok := record.Outcomes[outcomeKey(grant, effectID)]
	if !ok {
		return prior, ErrNotFound
	}
	if !sameGrant(prior.Grant, grant) {
		return prior, ErrFenced
	}
	if prior.State != "complete" || len(prior.Result) == 0 || !json.Valid(prior.Result) {
		return prior, ErrUncertain
	}
	return prior, nil
}

// ReadOutcome is historical evidence only. It cannot authorize another effect,
// and remains available after expiry, epoch advancement or retirement.
func (g *Gate) ReadOutcome(ctx context.Context, grant controlplane.ExecutionGrant, effectID string) (json.RawMessage, error) {
	_, record, release, err := g.locked(ctx, grant.Scope.TopologyUID)
	if err != nil {
		return nil, err
	}
	defer release()
	prior, err := completed(record, grant, effectID)
	if err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), prior.Result...), nil
}

// SealStop retires the exact completed grant and records its host-signed proof
// in the same ledger commit. Intent and unknown outcomes can never be sealed.
func (g *Gate) SealStop(ctx context.Context, grant controlplane.ExecutionGrant, effectID string, sign func(controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error)) (controlplane.SignedExecutionStopReceipt, error) {
	var zero controlplane.SignedExecutionStopReceipt
	path, record, release, err := g.locked(ctx, grant.Scope.TopologyUID)
	if err != nil {
		return zero, err
	}
	defer release()
	prior, err := completed(record, grant, effectID)
	if err != nil {
		return zero, err
	}
	if prior.Receipt != nil {
		return cloneReceipt(*prior.Receipt), nil
	}
	for _, other := range record.Outcomes {
		if other.Grant.Epoch == grant.Epoch {
			if !sameGrant(other.Grant, grant) {
				return zero, ErrFenced
			}
			if other.State != "complete" {
				return zero, ErrUncertain
			}
		}
	}
	if sign == nil {
		return zero, ErrFenced
	}
	receipt, err := sign(prior.Grant)
	if err != nil {
		return zero, err
	}
	if !receiptMatches(&receipt, grant) || receipt.HostBootID != g.BootID {
		return zero, ErrFenced
	}
	for key, other := range record.Outcomes {
		if sameGrant(other.Grant, grant) {
			copy := cloneReceipt(receipt)
			other.Receipt = &copy
			record.Outcomes[key] = other
		}
	}
	record.Stopped[grant.Epoch] = true
	if grant.Epoch > record.Epochs[grant.Scope.ResourceUID] {
		record.Epochs[grant.Scope.ResourceUID] = grant.Epoch
	}
	if err = save(path, record); err != nil {
		return zero, err
	}
	return cloneReceipt(receipt), nil
}
func cloneReceipt(r controlplane.SignedExecutionStopReceipt) controlplane.SignedExecutionStopReceipt {
	r.Signature = append([]byte(nil), r.Signature...)
	return r
}

// ReportStop retries only a previously persisted receipt. Network failure
// leaves it pending. The API can receive duplicates, always identical.
func (g *Gate) ReportStop(ctx context.Context, grant controlplane.ExecutionGrant, effectID string, report func(context.Context, controlplane.SignedExecutionStopReceipt) error) error {
	_, record, release, err := g.locked(ctx, grant.Scope.TopologyUID)
	if err != nil {
		return err
	}
	prior, err := completed(record, grant, effectID)
	if err != nil {
		release()
		return err
	}
	if prior.Receipt == nil || !record.Stopped[grant.Epoch] {
		release()
		return ErrUncertain
	}
	if prior.Reported {
		release()
		return nil
	}
	receipt := cloneReceipt(*prior.Receipt)
	release()
	if report == nil {
		return ErrFenced
	}
	if err = report(ctx, receipt); err != nil {
		return err
	}
	path, record, release, err := g.locked(ctx, grant.Scope.TopologyUID)
	if err != nil {
		return err
	}
	defer release()
	current, err := completed(record, grant, effectID)
	if err != nil {
		return err
	}
	a, _ := json.Marshal(current.Receipt)
	b, _ := json.Marshal(receipt)
	if !bytes.Equal(a, b) {
		return ErrFenced
	}
	if current.Reported {
		return nil
	}
	for key, other := range record.Outcomes {
		if sameGrant(other.Grant, grant) {
			other.Reported = true
			record.Outcomes[key] = other
		}
	}
	return save(path, record)
}

// RecoverStops visits completed outcomes only. It never retries a provider
// callback and does not turn an intent into a stop assertion. Each report has
// an independent budget so one unavailable API response cannot starve others.
func (g *Gate) RecoverStops(ctx context.Context, sign func(controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error), report func(context.Context, controlplane.SignedExecutionStopReceipt) error) error {
	return g.RecoverCompleted(ctx, func(ctx context.Context, grant controlplane.ExecutionGrant, id string) error {
		if _, err := g.SealStop(ctx, grant, id, sign); err != nil {
			return err
		}
		return g.ReportStop(ctx, grant, id, report)
	})
}

// RecoverCompleted visits only durable complete outcomes awaiting report.
// The callback runs outside the ledger lock and must re-read exact evidence
// before sealing it. Provider intents are never replayed by this scanner.
func (g *Gate) RecoverCompleted(ctx context.Context, recover func(context.Context, controlplane.ExecutionGrant, string) error) error {
	return g.recoverOutcomes(ctx, 2*time.Second, func(prior outcome) bool { return prior.State == "complete" && !prior.Reported }, recover)
}

func (g *Gate) recoverOutcomes(ctx context.Context, budget time.Duration, selectOutcome func(outcome) bool, recover func(context.Context, controlplane.ExecutionGrant, string) error) error {
	if recover == nil {
		return ErrFenced
	}
	entries, err := os.ReadDir(g.Root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		raw, err := os.ReadFile(filepath.Join(g.Root, entry.Name()))
		if err != nil {
			failures = append(failures, err)
			continue
		}
		var header struct {
			Version     int    `json:"version"`
			TopologyUID string `json:"topology_uid"`
		}
		if json.Unmarshal(raw, &header) != nil || header.Version != 2 || header.TopologyUID == "" || entry.Name() != fmt.Sprintf("%x.json", sha256.Sum256([]byte(header.TopologyUID))) {
			failures = append(failures, fmt.Errorf("%w: effect ledger requires explicit rebuild", ErrUncertain))
			continue
		}
		_, record, release, err := g.locked(ctx, header.TopologyUID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		keys := make([]string, 0, len(record.Outcomes))
		for key, prior := range record.Outcomes {
			if selectOutcome(prior) {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		type candidate struct {
			grant controlplane.ExecutionGrant
			id    string
		}
		candidates := make([]candidate, 0, len(keys))
		for _, key := range keys {
			prior := record.Outcomes[key]
			prefix := fmt.Sprintf("%d/", prior.Grant.Epoch)
			if !strings.HasPrefix(key, prefix) {
				failures = append(failures, ErrUncertain)
				continue
			}
			candidates = append(candidates, candidate{prior.Grant, strings.TrimPrefix(key, prefix)})
		}
		release()
		for _, candidate := range candidates {
			operation, cancel := context.WithCancel(ctx)
			if budget > 0 {
				cancel()
				operation, cancel = context.WithTimeout(ctx, budget)
			}
			err := recover(operation, candidate.grant, candidate.id)
			cancel()
			if err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}
