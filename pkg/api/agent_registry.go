package api

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type agentRegistry struct {
	mu          sync.RWMutex
	agents      map[string]controlplane.Agent
	streams     map[string]*Broadcaster
	status      map[string]*Broadcaster
	projections map[string]controlplane.Projection
	resources   map[string]controlplane.ResourceProjection
	events      map[string][]controlplane.AgentCommandEvent
}

func newAgentRegistry() *agentRegistry {
	return &agentRegistry{
		agents:      map[string]controlplane.Agent{},
		streams:     map[string]*Broadcaster{},
		status:      map[string]*Broadcaster{},
		projections: map[string]controlplane.Projection{},
		resources:   map[string]controlplane.ResourceProjection{},
		events:      map[string][]controlplane.AgentCommandEvent{},
	}
}

func (r *agentRegistry) SaveCommandEvent(event controlplane.AgentCommandEvent) {
	if event.AgentID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	items := append(r.events[event.AgentID], event)
	if len(items) > 512 {
		items = items[len(items)-512:]
	}
	r.events[event.AgentID] = items
}

func (r *agentRegistry) ListCommandEvents(agentID string) []controlplane.AgentCommandEvent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if agentID != "" {
		return append([]controlplane.AgentCommandEvent{}, r.events[agentID]...)
	}
	var out []controlplane.AgentCommandEvent
	for _, items := range r.events {
		out = append(out, items...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (r *agentRegistry) SaveResourceProjection(proj controlplane.ResourceProjection) {
	if proj.AgentID == "" || proj.Topology == "" {
		return
	}
	r.mu.Lock()
	key := proj.AgentID + "/" + proj.Workspace + "/" + proj.Topology
	r.resources[key] = proj
	stream := r.status[proj.Topology]
	r.mu.Unlock()
	if stream != nil {
		if raw, err := json.Marshal(proj); err == nil {
			_, _ = stream.Write(append(raw, '\n'))
		}
	}
}

func (r *agentRegistry) ListResourceProjections(topology string) []controlplane.ResourceProjection {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]controlplane.ResourceProjection, 0, len(r.resources))
	for _, proj := range r.resources {
		if topology == "" || proj.Topology == topology {
			out = append(out, proj)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Topology == out[j].Topology {
			return out[i].AgentID < out[j].AgentID
		}
		return out[i].Topology < out[j].Topology
	})
	return out
}

func (r *agentRegistry) SaveProjection(proj controlplane.Projection) {
	if proj.AgentID == "" || (proj.Workspace == "" && proj.Topology == "") {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := proj.AgentID + "/" + proj.Workspace + "/" + proj.Topology
	r.projections[key] = proj
}

func (r *agentRegistry) ListProjections(agentID string) []controlplane.Projection {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]controlplane.Projection, 0, len(r.projections))
	for _, proj := range r.projections {
		if agentID == "" || proj.AgentID == agentID {
			out = append(out, proj)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AgentID == out[j].AgentID {
			return out[i].Topology < out[j].Topology
		}
		return out[i].AgentID < out[j].AgentID
	})
	return out
}

func (r *agentRegistry) Save(agent controlplane.Agent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[agent.ID] = agent
}

func (r *agentRegistry) Get(id string) (*controlplane.Agent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	agent, ok := r.agents[id]
	if !ok {
		return nil, fmt.Errorf("agent not found")
	}
	return &agent, nil
}

func (r *agentRegistry) List() []controlplane.Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]controlplane.Agent, 0, len(r.agents))
	for _, agent := range r.agents {
		out = append(out, agent)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *agentRegistry) Stream(id string) *Broadcaster {
	r.mu.Lock()
	defer r.mu.Unlock()
	stream := r.streams[id]
	if stream == nil {
		stream = &Broadcaster{}
		r.streams[id] = stream
	}
	return stream
}

func (r *agentRegistry) StatusStream(topology string) *Broadcaster {
	r.mu.Lock()
	defer r.mu.Unlock()
	stream := r.status[topology]
	if stream == nil {
		stream = &Broadcaster{}
		r.status[topology] = stream
	}
	return stream
}

func (r *agentRegistry) PublishCommand(agentID string, cmd controlplane.AgentCommand) error {
	if cmd.ID == "" {
		cmd.ID = uuid.New().String()
	}
	if cmd.AgentID == "" {
		cmd.AgentID = agentID
	}
	if cmd.Status == "" {
		cmd.Status = controlplane.AgentCommandStatusQueued
	}
	if cmd.CreatedAt.IsZero() {
		cmd.CreatedAt = time.Now().UTC()
	}
	raw, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	_, err = r.Stream(agentID).Write(append(raw, '\n'))
	return err
}
