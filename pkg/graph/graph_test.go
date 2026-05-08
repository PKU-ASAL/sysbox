package graph

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildAndTopoWalk(t *testing.T) {
	g := New()

	g.AddNode("network", "dmz", nil)
	g.AddNode("image", "alpine", nil)
	g.AddNode("node", "web", []Ref{
		{Type: "network", Name: "dmz"},
		{Type: "image", Name: "alpine"},
	})

	order, err := g.TopoSort()
	require.NoError(t, err)

	webIdx := indexOf(order, "node", "web")
	netIdx := indexOf(order, "network", "dmz")
	imgIdx := indexOf(order, "image", "alpine")
	require.Greater(t, webIdx, netIdx)
	require.Greater(t, webIdx, imgIdx)
}

func TestCycleDetection(t *testing.T) {
	g := New()
	g.AddNode("a", "1", []Ref{{Type: "b", Name: "1"}})
	g.AddNode("b", "1", []Ref{{Type: "a", Name: "1"}})

	_, err := g.TopoSort()
	require.Error(t, err)
	require.Contains(t, err.Error(), "cycle")
}

func TestUnknownReference(t *testing.T) {
	g := New()
	g.AddNode("node", "web", []Ref{{Type: "missing", Name: "x"}})
	_, err := g.TopoSort()
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown")
}

func TestReverseWalk(t *testing.T) {
	g := New()
	g.AddNode("network", "dmz", nil)
	g.AddNode("node", "web", []Ref{{Type: "network", Name: "dmz"}})

	order, err := g.ReverseTopoSort()
	require.NoError(t, err)

	webIdx := indexOf(order, "node", "web")
	netIdx := indexOf(order, "network", "dmz")
	require.Less(t, webIdx, netIdx)
}

func indexOf(order []NodeID, typ, name string) int {
	for i, id := range order {
		if id.Type == typ && id.Name == name {
			return i
		}
	}
	return -1
}
