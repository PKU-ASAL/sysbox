// Package graph builds a resource DAG from parsed HCL and walks it
// in topological order (for apply) or reverse topological order (for destroy).
package graph

import (
	"fmt"
)

type NodeID struct {
	Type string
	Name string
}

func (id NodeID) String() string {
	return fmt.Sprintf("%s.%s", id.Type, id.Name)
}

type Ref = NodeID

type Node struct {
	ID   NodeID
	Deps []Ref
	Data any
}

type Graph struct {
	nodes map[NodeID]*Node
}

func New() *Graph {
	return &Graph{nodes: make(map[NodeID]*Node)}
}

func (g *Graph) AddNode(typ, name string, deps []Ref) *Node {
	id := NodeID{Type: typ, Name: name}
	n := &Node{ID: id, Deps: deps}
	g.nodes[id] = n
	return n
}

func (g *Graph) SetData(typ, name string, data any) {
	if n, ok := g.nodes[NodeID{typ, name}]; ok {
		n.Data = data
	}
}

func (g *Graph) Get(typ, name string) *Node {
	return g.nodes[NodeID{typ, name}]
}

func (g *Graph) All() []*Node {
	out := make([]*Node, 0, len(g.nodes))
	for _, n := range g.nodes {
		out = append(out, n)
	}
	return out
}

// Validate checks the graph for dangling references and cycles.
// Returns the first error encountered, or nil if the graph is valid.
// This should be called before TopoSort so users get a clear error
// message instead of a cryptic sort failure.
func (g *Graph) Validate() error {
	// Check for dangling references.
	for id, n := range g.nodes {
		for _, dep := range n.Deps {
			if _, ok := g.nodes[dep]; !ok {
				return fmt.Errorf("resource %s references unknown %s", id, dep)
			}
		}
	}
	// Check for cycles using DFS with three-colour marking.
	const (
		white = 0 // unvisited
		gray  = 1 // in current path
		black = 2 // fully explored
	)
	color := make(map[NodeID]int, len(g.nodes))
	var visit func(NodeID) error
	visit = func(id NodeID) error {
		switch color[id] {
		case gray:
			return fmt.Errorf("cycle detected at %s", id)
		case black:
			return nil
		}
		color[id] = gray
		n := g.nodes[id]
		if n != nil {
			for _, dep := range n.Deps {
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		color[id] = black
		return nil
	}
	for id := range g.nodes {
		if color[id] == white {
			if err := visit(id); err != nil {
				return err
			}
		}
	}
	return nil
}
