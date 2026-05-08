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
