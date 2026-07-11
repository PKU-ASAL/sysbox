// Package graph builds and validates the resource dependency graph.
package graph

import (
	"fmt"
	"sort"

	"github.com/oslab/sysbox/pkg/address"
)

type Node struct {
	Address address.Address
	Deps    []address.Address
	Data    any
}

type Graph struct {
	nodes map[string]*Node
}

func New() *Graph {
	return &Graph{nodes: make(map[string]*Node)}
}

func (g *Graph) AddNode(addr address.Address, deps []address.Address) error {
	key := addr.String()
	if _, exists := g.nodes[key]; exists {
		return fmt.Errorf("duplicate resource %s", addr)
	}
	ownedDeps := append([]address.Address(nil), deps...)
	g.nodes[key] = &Node{Address: addr, Deps: ownedDeps}
	return nil
}

func (g *Graph) SetData(addr address.Address, data any) error {
	node := g.Get(addr)
	if node == nil {
		return fmt.Errorf("resource %s is not in graph", addr)
	}
	node.Data = data
	return nil
}

func (g *Graph) Get(addr address.Address) *Node {
	return g.nodes[addr.String()]
}

func (g *Graph) All() []*Node {
	out := make([]*Node, 0, len(g.nodes))
	for _, node := range g.nodes {
		out = append(out, node)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Address.Less(out[j].Address)
	})
	return out
}

func (g *Graph) Validate() error {
	for _, node := range g.nodes {
		for _, dep := range node.Deps {
			if g.Get(dep) == nil {
				return fmt.Errorf("resource %s references unknown %s", node.Address, dep)
			}
		}
	}

	const (
		white = iota
		gray
		black
	)
	colors := make(map[string]int, len(g.nodes))
	var visit func(address.Address) error
	visit = func(addr address.Address) error {
		key := addr.String()
		switch colors[key] {
		case gray:
			return fmt.Errorf("cycle detected at %s", addr)
		case black:
			return nil
		}
		colors[key] = gray
		if node := g.Get(addr); node != nil {
			for _, dep := range node.Deps {
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		colors[key] = black
		return nil
	}
	for _, node := range g.All() {
		if colors[node.Address.String()] == white {
			if err := visit(node.Address); err != nil {
				return err
			}
		}
	}
	return nil
}
