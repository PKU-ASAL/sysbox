package api

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type agentRegistry struct {
	mu          sync.RWMutex
	agents      map[string]controlplane.Agent
	streams     map[string]*Broadcaster
	projections map[string]controlplane.Projection
}

func newAgentRegistry() *agentRegistry {
	return &agentRegistry{
		agents:      map[string]controlplane.Agent{},
		streams:     map[string]*Broadcaster{},
		projections: map[string]controlplane.Projection{},
	}
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

func (r *agentRegistry) PublishRun(agentID string, run controlplane.Run) error {
	raw, err := json.Marshal(agentCommand{Type: "run_assigned", Run: &run})
	if err != nil {
		return err
	}
	_, err = r.Stream(agentID).Write(append(raw, '\n'))
	return err
}

type agentCommand struct {
	Type string            `json:"type"`
	Run  *controlplane.Run `json:"run,omitempty"`
}
