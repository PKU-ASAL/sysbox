package api

import (
	"fmt"
	"sort"
	"sync"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type agentRegistry struct {
	mu     sync.RWMutex
	agents map[string]controlplane.Worker
}

func newAgentRegistry() *agentRegistry {
	return &agentRegistry{agents: map[string]controlplane.Worker{}}
}

func (r *agentRegistry) Save(agent controlplane.Worker) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[agent.ID] = agent
}

func (r *agentRegistry) Get(id string) (*controlplane.Worker, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	agent, ok := r.agents[id]
	if !ok {
		return nil, fmt.Errorf("agent not found")
	}
	return &agent, nil
}

func (r *agentRegistry) List() []controlplane.Worker {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]controlplane.Worker, 0, len(r.agents))
	for _, agent := range r.agents {
		out = append(out, agent)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
