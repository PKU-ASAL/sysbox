package graph

import (
	"fmt"
	"sort"
)

// TopoSort returns node IDs in dependency order (deps before dependents).
// Returns an error if the graph contains a cycle.
//
// To make output deterministic across runs, ties at each "ready" frontier
// are broken by NodeID.String().
func (g *Graph) TopoSort() ([]NodeID, error) {
	inDegree := make(map[NodeID]int)
	neighbors := make(map[NodeID][]NodeID)

	for id := range g.nodes {
		inDegree[id] = 0
	}

	for id, n := range g.nodes {
		for _, dep := range n.Deps {
			if _, ok := g.nodes[dep]; !ok {
				return nil, fmt.Errorf("resource %s references unknown %s", id, dep)
			}
			neighbors[dep] = append(neighbors[dep], id)
			inDegree[id]++
		}
	}

	var queue []NodeID
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}
	sortNodeIDs(queue)

	var order []NodeID
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		order = append(order, cur)

		var freed []NodeID
		for _, next := range neighbors[cur] {
			inDegree[next]--
			if inDegree[next] == 0 {
				freed = append(freed, next)
			}
		}
		sortNodeIDs(freed)
		queue = append(queue, freed...)
	}

	if len(order) != len(g.nodes) {
		return nil, fmt.Errorf("graph has cycle: resolved %d of %d nodes", len(order), len(g.nodes))
	}
	return order, nil
}

// ReverseTopoSort is TopoSort reversed (dependents before deps).
// Used by destroy: tear down dependents first.
func (g *Graph) ReverseTopoSort() ([]NodeID, error) {
	order, err := g.TopoSort()
	if err != nil {
		return nil, err
	}
	reversed := make([]NodeID, len(order))
	for i, id := range order {
		reversed[len(order)-1-i] = id
	}
	return reversed, nil
}

func sortNodeIDs(ids []NodeID) {
	sort.Slice(ids, func(i, j int) bool {
		return ids[i].String() < ids[j].String()
	})
}
