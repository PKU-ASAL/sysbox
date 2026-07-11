package graph

import (
	"fmt"
	"sort"

	"github.com/oslab/sysbox/pkg/address"
)

func (g *Graph) TopoSort() ([]address.Address, error) {
	inDegree := make(map[string]int, len(g.nodes))
	neighbors := make(map[string][]address.Address, len(g.nodes))
	addresses := make(map[string]address.Address, len(g.nodes))
	for _, node := range g.nodes {
		key := node.Address.String()
		inDegree[key] = 0
		addresses[key] = node.Address
	}
	for _, node := range g.nodes {
		for _, dep := range node.Deps {
			if g.Get(dep) == nil {
				return nil, fmt.Errorf("resource %s references unknown %s", node.Address, dep)
			}
			neighbors[dep.String()] = append(neighbors[dep.String()], node.Address)
			inDegree[node.Address.String()]++
		}
	}

	queue := make([]address.Address, 0)
	for key, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, addresses[key])
		}
	}
	sortAddresses(queue)

	order := make([]address.Address, 0, len(g.nodes))
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		order = append(order, current)

		freed := make([]address.Address, 0)
		for _, next := range neighbors[current.String()] {
			key := next.String()
			inDegree[key]--
			if inDegree[key] == 0 {
				freed = append(freed, next)
			}
		}
		sortAddresses(freed)
		queue = append(queue, freed...)
	}
	if len(order) != len(g.nodes) {
		return nil, fmt.Errorf("graph has cycle: resolved %d of %d nodes", len(order), len(g.nodes))
	}
	return order, nil
}

func (g *Graph) ReverseTopoSort() ([]address.Address, error) {
	order, err := g.TopoSort()
	if err != nil {
		return nil, err
	}
	reversed := make([]address.Address, len(order))
	for i, addr := range order {
		reversed[len(order)-1-i] = addr
	}
	return reversed, nil
}

func sortAddresses(addresses []address.Address) {
	sort.Slice(addresses, func(i, j int) bool {
		return addresses[i].Less(addresses[j])
	})
}
