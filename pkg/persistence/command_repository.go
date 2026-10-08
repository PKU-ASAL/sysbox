package persistence

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type CommandRecord struct {
	Recovery *controlplane.RunRecoveryReport
	Command  controlplane.AgentCommand
	Retired  bool
}
type CommandStorage interface {
	ReadCommand(context.Context, string) (*CommandRecord, error)
	ListCommands(context.Context, string) ([]CommandRecord, error)
	// UpdateCommand serializes read/transform/write across processes. Missing
	// records are nil; a nil replacement leaves the current record unchanged.
	// Return a record only after commit succeeds, never a speculative grant.
	UpdateCommand(context.Context, string, func(*CommandRecord) (*CommandRecord, error)) (*CommandRecord, error)
}
type CommandRepository struct {
	storage CommandStorage
	now     func() time.Time
}

func NewCommandRepository(storage CommandStorage) *CommandRepository {
	return &CommandRepository{storage: storage, now: func() time.Time { return time.Now().UTC() }}
}

func DurableCommand(cmd controlplane.AgentCommand) controlplane.AgentCommand {
	cmd.CreatedAt = cmd.CreatedAt.UTC()
	cmd.Delivered = cmd.Delivered.UTC()
	cmd.AckedAt = cmd.AckedAt.UTC()
	cmd.EndedAt = cmd.EndedAt.UTC()
	cmd.LeaseUntil = cmd.LeaseUntil.UTC()
	if cmd.AgentID == "" {
		cmd.AgentID = "_unknown"
	}
	if cmd.Status == "" {
		cmd.Status = controlplane.AgentCommandStatusQueued
	}
	if cmd.Protocol == "" {
		cmd.Protocol = controlplane.AgentProtocolVersion
	}
	if cmd.Run != nil {
		run := DurableRun(*cmd.Run)
		cmd.Run = &run
	}
	return cmd
}

type storedCommand struct {
	Recovery *controlplane.RunRecoveryReport `json:"recovery_report,omitempty"`
	controlplane.AgentCommand
	Retired bool `json:"retired,omitempty"`
}

func EncodeCommandRecord(record CommandRecord) ([]byte, error) {
	return json.Marshal(storedCommand{AgentCommand: DurableCommand(record.Command), Retired: record.Retired, Recovery: record.Recovery})
}
func DecodeCommandRecord(raw []byte) (*CommandRecord, error) {
	var stored storedCommand
	if err := decodeGuestObject(raw, &stored); err != nil {
		return nil, err
	}
	record := &CommandRecord{Command: DurableCommand(stored.AgentCommand), Retired: stored.Retired, Recovery: stored.Recovery}
	if validateCommand(record.Command) != nil {
		return nil, ErrCorruptRecord
	}
	return record, nil
}
func validateCommand(cmd controlplane.AgentCommand) error {
	if !topologyName.MatchString(cmd.ID) || !topologyName.MatchString(cmd.AgentID) || cmd.Attempt < 0 || cmd.Protocol != controlplane.AgentProtocolVersion {
		return ErrInvalidRecord
	}
	switch cmd.Status {
	case controlplane.AgentCommandStatusQueued, controlplane.AgentCommandStatusLeased, controlplane.AgentCommandStatusDelivered, controlplane.AgentCommandStatusAcknowledged, controlplane.AgentCommandStatusRunning, controlplane.AgentCommandStatusCompleted, controlplane.AgentCommandStatusFailed, controlplane.AgentCommandStatusDenied, controlplane.AgentCommandStatusCancelled:
		return nil
	}
	return ErrInvalidRecord
}
func commandPayload(cmd controlplane.AgentCommand) controlplane.AgentCommand {
	cmd = DurableCommand(cmd)
	cmd.Status = ""
	cmd.Err = ""
	cmd.CreatedAt = time.Time{}
	cmd.Delivered = time.Time{}
	cmd.AckedAt = time.Time{}
	cmd.EndedAt = time.Time{}
	cmd.LeaseOwner = ""
	cmd.LeaseUntil = time.Time{}
	cmd.Attempt = 0
	return cmd
}
func mergeCommandPatch(old, next controlplane.AgentCommand) controlplane.AgentCommand {
	if next.RecoveryMode == "" {
		next.RecoveryMode = old.RecoveryMode
	}
	if next.Type == "" {
		next.Type = old.Type
	}
	if next.Run == nil {
		next.Run = old.Run
	}
	if next.Session == nil {
		next.Session = old.Session
	}
	if next.Execution == nil {
		next.Execution = old.Execution
	}
	if next.FilePut == nil {
		next.FilePut = old.FilePut
	}
	if next.GCKind == "" {
		next.GCKind = old.GCKind
	}
	if next.GCExternalID == "" {
		next.GCExternalID = old.GCExternalID
	}
	if next.GCInventoryDigest == "" {
		next.GCInventoryDigest = old.GCInventoryDigest
	}
	if next.Operation.ID == "" && next.Operation.Operation == "" && next.Operation.ExternalID == "" {
		next.Operation = old.Operation
	}
	if len(next.ExecutionRequest.Argv) == 0 {
		next.ExecutionRequest = old.ExecutionRequest
	}
	if len(next.Request.Cmd) == 0 && next.Request.Shell == "" && len(next.Request.Env) == 0 && next.Request.WorkDir == "" && next.Request.TTY == nil && next.Request.TimeoutSeconds == 0 {
		next.Request = old.Request
	}
	next.CreatedAt = old.CreatedAt
	return next
}
func (r *CommandRepository) SaveAgentCommand(ctx context.Context, cmd controlplane.AgentCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd = DurableCommand(cmd)
	if err := validateCommand(cmd); err != nil {
		return err
	}
	_, err := r.storage.UpdateCommand(ctx, cmd.ID, func(current *CommandRecord) (*CommandRecord, error) {
		if current == nil {
			return &CommandRecord{Command: cmd}, nil
		}
		if current.Retired {
			return nil, ErrConflict
		}
		old := current.Command
		if validateCommand(old) != nil {
			return nil, ErrCorruptRecord
		}
		if old.ID != cmd.ID || old.AgentID != cmd.AgentID {
			return nil, ErrConflict
		}
		next := mergeCommandPatch(old, cmd)
		a, ea := json.Marshal(commandPayload(old))
		b, eb := json.Marshal(commandPayload(next))
		if ea != nil || eb != nil || string(a) != string(b) {
			return nil, ErrConflict
		}
		if !controlplane.CanAdvanceAgentCommandStatus(old.Status, next.Status) {
			return nil, nil
		}
		// A status-only cancellation is an explicit control-plane action. Other
		// updates must carry the currently granted delivery lease unchanged.
		cancelPatch := next.Status == controlplane.AgentCommandStatusCancelled && cmd.LeaseOwner == "" && cmd.Attempt == 0
		if !cancelPatch && (old.LeaseOwner != next.LeaseOwner || old.Attempt != next.Attempt || !old.LeaseUntil.Equal(next.LeaseUntil)) {
			return nil, ErrConflict
		}
		if cancelPatch {
			next.LeaseOwner = old.LeaseOwner
			next.LeaseUntil = old.LeaseUntil
			next.Attempt = old.Attempt
		}
		if old.IsTerminal() {
			return nil, nil
		}
		return &CommandRecord{Command: next}, nil
	})
	return err
}
func (r *CommandRepository) GetAgentCommand(ctx context.Context, agent, id string) (*controlplane.AgentCommand, error) {
	if !topologyName.MatchString(id) || !topologyName.MatchString(agent) {
		return nil, ErrInvalidRecord
	}
	record, err := r.storage.ReadCommand(ctx, id)
	if err != nil {
		return nil, err
	}
	if record == nil || record.Retired || record.Command.AgentID != agent {
		return nil, ErrNotFound
	}
	if record.Command.ID != id || validateCommand(record.Command) != nil {
		return nil, ErrCorruptRecord
	}
	cmd := record.Command
	return &cmd, nil
}
func (r *CommandRepository) ListAgentCommands(ctx context.Context, agent string) ([]controlplane.AgentCommand, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if agent != "" && !topologyName.MatchString(agent) {
		return nil, ErrInvalidRecord
	}
	records, err := r.storage.ListCommands(ctx, agent)
	if err != nil {
		return nil, err
	}
	out := make([]controlplane.AgentCommand, 0, len(records))
	seen := map[string]bool{}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cmd := record.Command
		if validateCommand(cmd) != nil || seen[cmd.ID] {
			return nil, ErrCorruptRecord
		}
		seen[cmd.ID] = true
		if !record.Retired && (agent == "" || cmd.AgentID == agent) {
			out = append(out, cmd)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (r *CommandRepository) AcquireAgentCommandLease(ctx context.Context, agent, id, owner string, ttl time.Duration) (*controlplane.AgentCommand, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !topologyName.MatchString(agent) || !topologyName.MatchString(id) || strings.TrimSpace(owner) == "" || ttl <= 0 {
		return nil, false, ErrInvalidRecord
	}
	granted := false
	record, err := r.storage.UpdateCommand(ctx, id, func(current *CommandRecord) (*CommandRecord, error) {
		if current == nil || current.Retired || current.Command.AgentID != agent {
			return nil, ErrNotFound
		}
		cmd := current.Command
		if validateCommand(cmd) != nil {
			return nil, ErrCorruptRecord
		}
		now := r.now()
		if !cmd.Leasable(now) {
			return nil, nil
		}
		if cmd.Attempt == int(^uint(0)>>1) {
			return nil, ErrConflict
		}
		cmd.MarkLeased(owner, ttl, now)
		granted = true
		return &CommandRecord{Command: cmd}, nil
	})
	if err != nil {
		return nil, false, err
	}
	if record == nil {
		return nil, false, ErrNotFound
	}
	cmd := record.Command
	return &cmd, granted, nil
}

// PrepareAgentCommandDelivery rechecks delivery authority inside the storage
// transaction. A successful no-op Save is not permission to send a stale copy.
// This fences queue delivery only, not resource-side effects after transmission.
func (r *CommandRepository) PrepareAgentCommandDelivery(ctx context.Context, claimed controlplane.AgentCommand) (*controlplane.AgentCommand, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !topologyName.MatchString(claimed.ID) || !topologyName.MatchString(claimed.AgentID) {
		return nil, ErrInvalidRecord
	}
	record, err := r.storage.UpdateCommand(ctx, claimed.ID, func(current *CommandRecord) (*CommandRecord, error) {
		if current == nil || current.Retired {
			return nil, ErrNotFound
		}
		cmd := current.Command
		if validateCommand(cmd) != nil {
			return nil, ErrCorruptRecord
		}
		if cmd.AgentID != claimed.AgentID || cmd.Status != controlplane.AgentCommandStatusLeased || cmd.LeaseOwner == "" || cmd.Attempt < 1 || cmd.LeaseOwner != claimed.LeaseOwner || cmd.Attempt != claimed.Attempt || !cmd.LeaseUntil.Equal(claimed.LeaseUntil) || !cmd.LeaseUntil.After(r.now()) {
			return nil, ErrConflict
		}
		cmd.MarkDelivered(r.now())
		return &CommandRecord{Command: cmd}, nil
	})
	if err != nil {
		return nil, err
	}
	cmd := record.Command
	return &cmd, nil
}

func (r *CommandRepository) DeleteAgentCommand(ctx context.Context, agent, id string) error {
	if !topologyName.MatchString(id) || !topologyName.MatchString(agent) {
		return ErrInvalidRecord
	}
	_, err := r.storage.UpdateCommand(ctx, id, func(current *CommandRecord) (*CommandRecord, error) {
		if current == nil {
			return nil, nil
		}
		if current.Command.AgentID != agent {
			return nil, ErrNotFound
		}
		if current.Retired {
			return nil, nil
		}
		next := *current
		next.Retired = true
		if !next.Command.IsTerminal() {
			next.Command.Status = controlplane.AgentCommandStatusCancelled
			next.Command.EndedAt = r.now()
		}
		// Host completion can outlive worker dispatch. Preserve its immutable
		// Run incarnation; DurableCommand has already stripped transient Inputs.
		if next.Command.Type != "run_assigned" {
			next.Command.Run = nil
		}
		next.Command.Session = nil
		next.Command.Request = controlplane.ConsoleRequest{}
		next.Command.Operation = controlplane.NodeOperation{}
		next.Command.Execution = nil
		next.Command.ExecutionRequest = controlplane.GuestExecutionRequest{}
		next.Command.FilePut = nil
		return &next, nil
	})
	return err
}
