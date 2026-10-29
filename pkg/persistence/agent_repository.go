package persistence

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"sort"
	"time"
)

// AgentStorage owns physical atomicity, not registration or liveness policy.
// Missing records are nil; nil replacements preserve current data. Update must
// return the committed record, never a speculative result after a failed write.
type AgentStorage interface {
	ReadAgent(context.Context, string) (*controlplane.Agent, error)
	ListAgents(context.Context) ([]controlplane.Agent, error)
	UpdateAgent(context.Context, string, func(*controlplane.Agent) (*controlplane.Agent, error)) (*controlplane.Agent, error)
}
type AgentRepository struct {
	storage AgentStorage
	now     func() time.Time
}

func NewAgentRepository(storage AgentStorage) *AgentRepository {
	return &AgentRepository{storage: storage, now: func() time.Time { return time.Now().UTC() }}
}

// AgentPolicy changes only the explicitly supplied administrative flags.
type AgentPolicy struct {
	Disabled    *bool  `json:"disabled,omitempty"`
	Quarantined *bool  `json:"quarantined,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

func normalizeAgentRecord(agent controlplane.Agent) controlplane.Agent {
	if agent.Protocol == "" {
		agent.Protocol = controlplane.AgentProtocolVersion
	}
	if agent.Status == "" {
		agent.Status = controlplane.AgentStatusOnline
	}
	agent.LastHeartbeat = agent.LastHeartbeat.UTC()
	agent.CreatedAt = agent.CreatedAt.UTC()
	agent.UpdatedAt = agent.UpdatedAt.UTC()
	return agent
}
func validateAgentRecord(agent controlplane.Agent) error {
	if !topologyName.MatchString(agent.ID) {
		return ErrInvalidRecord
	}
	if agent.Protocol != controlplane.AgentProtocolVersion {
		return fmt.Errorf("%w: unsupported agent protocol", ErrInvalidRecord)
	}
	switch agent.Status {
	case controlplane.AgentStatusOnline, controlplane.AgentStatusOffline, controlplane.AgentStatusDisabled, controlplane.AgentStatusQuarantined:
		return nil
	}
	return ErrInvalidRecord
}
func EncodeAgentRecord(agent controlplane.Agent) ([]byte, error) {
	agent = normalizeAgentRecord(agent)
	if err := validateAgentRecord(agent); err != nil {
		return nil, err
	}
	return json.Marshal(agent)
}
func DecodeAgentRecord(raw []byte) (*controlplane.Agent, error) {
	var agent controlplane.Agent
	if err := decodeGuestObject(raw, &agent); err != nil {
		return nil, err
	}
	if validateAgentRecord(agent) != nil {
		return nil, ErrCorruptRecord
	}
	agent = normalizeAgentRecord(agent)
	return &agent, nil
}

// SaveAgent is explicit registration/admin replacement. Liveness and policy
// callers must use the narrower atomic operations below, never read/merge/Save.
func (r *AgentRepository) SaveAgent(ctx context.Context, agent controlplane.Agent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	agent = normalizeAgentRecord(agent)
	if err := validateAgentRecord(agent); err != nil {
		return err
	}
	_, err := r.storage.UpdateAgent(ctx, agent.ID, func(current *controlplane.Agent) (*controlplane.Agent, error) {
		if current != nil {
			if current.ID != agent.ID || validateAgentRecord(*current) != nil {
				return nil, ErrCorruptRecord
			}
			agent.CreatedAt = current.CreatedAt
		}
		return &agent, nil
	})
	return err
}
func (r *AgentRepository) GetAgent(ctx context.Context, id string) (*controlplane.Agent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !topologyName.MatchString(id) {
		return nil, ErrInvalidRecord
	}
	agent, err := r.storage.ReadAgent(ctx, id)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, ErrNotFound
	}
	if agent.ID != id || validateAgentRecord(*agent) != nil {
		return nil, ErrCorruptRecord
	}
	return agent, nil
}
func (r *AgentRepository) ListAgents(ctx context.Context) ([]controlplane.Agent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	agents, err := r.storage.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]controlplane.Agent, 0, len(agents))
	seen := map[string]bool{}
	for _, agent := range agents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if validateAgentRecord(agent) != nil || seen[agent.ID] {
			return nil, ErrCorruptRecord
		}
		seen[agent.ID] = true
		out = append(out, agent)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (r *AgentRepository) update(ctx context.Context, id string, change func(controlplane.Agent) (*controlplane.Agent, error)) (*controlplane.Agent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !topologyName.MatchString(id) {
		return nil, ErrInvalidRecord
	}
	return r.storage.UpdateAgent(ctx, id, func(current *controlplane.Agent) (*controlplane.Agent, error) {
		if current == nil {
			return nil, ErrNotFound
		}
		if current.ID != id || validateAgentRecord(*current) != nil {
			return nil, ErrCorruptRecord
		}
		return change(*current)
	})
}

// RegisterAgent creates an identity or refreshes its self-reported metadata.
// Existing policy and credentials are immutable through this operation.
func (r *AgentRepository) RegisterAgent(ctx context.Context, report controlplane.Agent) (*controlplane.Agent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if report.ID == "" {
		report.ID = report.Name
	}
	report = normalizeAgentRecord(report)
	if err := validateAgentRecord(report); err != nil {
		return nil, err
	}
	return r.storage.UpdateAgent(ctx, report.ID, func(current *controlplane.Agent) (*controlplane.Agent, error) {
		now := r.now()
		if current == nil {
			if report.Name == "" {
				report.Name = report.ID
			}
			report.CreatedAt = now
			report.UpdatedAt = now
			report.LastHeartbeat = now
			return &report, nil
		}
		if current.ID != report.ID || validateAgentRecord(*current) != nil {
			return nil, ErrCorruptRecord
		}
		// A public hash or an omitted secret is not proof of identity. Keep
		// legacy unprotected registration, but never refresh a protected agent
		// unless the actual current credential matches under this same lock.
		if current.SecretHash != "" && current.AuthSecret == "" {
			return nil, ErrConflict
		}
		if subtle.ConstantTimeCompare([]byte(report.AuthSecret), []byte(current.AuthSecret)) != 1 || report.SecretHash != "" && report.SecretHash != current.SecretHash {
			return nil, ErrConflict
		}
		next := applyAgentReport(*current, report, now)
		return &next, nil
	})
}

func applyAgentReport(current, report controlplane.Agent, now time.Time) controlplane.Agent {
	if report.Name != "" {
		current.Name = report.Name
	}
	// Scheduling authority is fixed by administrator enrollment. A worker
	// heartbeat or reconnect cannot enlarge its capabilities or placement labels.
	if report.Version != "" {
		current.Version = report.Version
	}
	if current.Disabled || current.Quarantined {
		current.Status = controlplane.AgentStatusForPolicy(current.Disabled, current.Quarantined)
	} else if !current.IsBlocked() {
		current.Status = controlplane.AgentStatusOnline
		current.Reason = ""
	}
	if now.After(current.LastHeartbeat) {
		current.LastHeartbeat = now
	}
	if now.After(current.UpdatedAt) {
		current.UpdatedAt = now
	}
	return current
}

func (r *AgentRepository) HeartbeatAgent(ctx context.Context, report controlplane.Agent) (*controlplane.Agent, error) {
	if report.Protocol != "" && report.Protocol != controlplane.AgentProtocolVersion {
		return nil, fmt.Errorf("%w: unsupported agent protocol", ErrInvalidRecord)
	}
	return r.update(ctx, report.ID, func(current controlplane.Agent) (*controlplane.Agent, error) {
		next := applyAgentReport(current, report, r.now())
		return &next, nil
	})
}
func (r *AgentRepository) UpdateAgentPolicy(ctx context.Context, id string, patch AgentPolicy) (*controlplane.Agent, error) {
	return r.update(ctx, id, func(current controlplane.Agent) (*controlplane.Agent, error) {
		// Normalize old status-only blocks before changing one optional flag.
		if current.Status == controlplane.AgentStatusDisabled {
			current.Disabled = true
		}
		if current.Status == controlplane.AgentStatusQuarantined {
			current.Quarantined = true
		}
		if patch.Disabled != nil {
			current.Disabled = *patch.Disabled
		}
		if patch.Quarantined != nil {
			current.Quarantined = *patch.Quarantined
		}
		if patch.Reason != "" {
			current.Reason = patch.Reason
		}
		if current.Disabled || current.Quarantined {
			current.Status = controlplane.AgentStatusForPolicy(current.Disabled, current.Quarantined)
		} else if current.Status != controlplane.AgentStatusOffline {
			current.Status = controlplane.AgentStatusOnline
			current.Reason = ""
		}
		now := r.now()
		if now.After(current.UpdatedAt) {
			current.UpdatedAt = now
		}
		return &current, nil
	})
}
func (r *AgentRepository) ExpireAgentHeartbeat(ctx context.Context, id string, observedAt time.Time, ttl time.Duration) (*controlplane.Agent, error) {
	if ttl <= 0 || observedAt.IsZero() {
		return nil, ErrInvalidRecord
	}
	return r.update(ctx, id, func(current controlplane.Agent) (*controlplane.Agent, error) {
		if current.IsBlocked() || current.Status == controlplane.AgentStatusOffline || current.LastHeartbeat.IsZero() || !current.LastHeartbeat.Before(observedAt.Add(-ttl)) {
			return nil, nil
		}
		current.Status = controlplane.AgentStatusOffline
		current.Reason = "heartbeat stale"
		if observedAt.After(current.UpdatedAt) {
			current.UpdatedAt = observedAt.UTC()
		}
		return &current, nil
	})
}
