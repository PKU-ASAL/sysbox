package api

import (
	"net/http"
	"strings"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

// graphNode is the visualization-friendly JSON shape for a resource.
type graphNode struct {
	ID        string         `json:"id"`     // "sysbox_node.web"
	Type      string         `json:"type"`   // "sysbox_node"
	Label     string         `json:"label"`  // "web"
	Status    string         `json:"status"` // "applied" | "planned"
	Substrate string         `json:"substrate,omitempty"`
	IP        string         `json:"ip,omitempty"`
	CIDR      string         `json:"cidr,omitempty"`
	NAT       bool           `json:"nat,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

// graphEdge is a typed link between two graph nodes.
type graphEdge struct {
	From  string `json:"from"` // "sysbox_node.web"
	To    string `json:"to"`   // "sysbox_network.lan"
	Kind  string `json:"kind"` // "link" | "interface" | "image" | "kernel" | "hosts"
	Label string `json:"label,omitempty"`
	IP    string `json:"ip,omitempty"`
}

// GET /v1/topologies/{topology}/graph
// Returns a visualization-ready {nodes, edges} JSON document built from
// the HCL workspace + (optional) state file. When state is present, runtime
// info such as primary_ip is merged into the node payload and status flips
// from "planned" to "applied".
func (s *Server) handleGetGraph(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	mgr, err := s.stateManager(topology)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	g, _, st, _, _, err := runtime.LoadWorkspaceWithManager(s.hclFile(topology), mgr)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	stateByID := map[string]*state.Resource{}
	if st != nil {
		for i := range st.Resources {
			r := &st.Resources[i]
			stateByID[r.Type+"."+r.Name] = r
		}
	}

	nodes := make([]graphNode, 0, len(g.All()))
	edges := make([]graphEdge, 0)

	// Build a lookup from (from→to) to resolved link IPs / interface
	// labels so we can enrich dependency edges with config details.
	type linkInfo struct {
		IP    string
		Label string // interface name for routers
	}
	linkMap := map[string][]linkInfo{} // key = "fromType.fromName→depType.depName"
	addLink := func(from, to graph.NodeID, ip, label string) {
		key := from.String() + "→" + to.String()
		linkMap[key] = append(linkMap[key], linkInfo{IP: stripCIDRSuffix(ip), Label: label})
	}

	for _, n := range g.All() {
		switch cfg := n.Data.(type) {
		case *config.NodeConfig:
			for _, link := range cfg.Links {
				if ref := config.ResolveName(link.Network); ref != "" {
					addLink(n.ID, graph.NodeID{Type: "sysbox_network", Name: ref}, link.IP, "")
				}
			}
		case *config.RouterConfig:
			for _, iface := range cfg.Interfaces {
				if ref := config.ResolveName(iface.Network); ref != "" {
					addLink(n.ID, graph.NodeID{Type: "sysbox_network", Name: ref}, iface.IP, iface.Name)
				}
			}
		case *config.ActorConfig:
			for _, link := range cfg.Links {
				if ref := config.ResolveName(link.Network); ref != "" {
					addLink(n.ID, graph.NodeID{Type: "sysbox_network", Name: ref}, link.IP, "")
				}
			}
		}
	}

	for _, n := range g.All() {
		gn := graphNode{
			ID:     n.ID.String(),
			Type:   n.ID.Type,
			Label:  n.ID.Name,
			Status: "planned",
		}
		if rs, ok := stateByID[n.ID.String()]; ok {
			gn.Status = "applied"
			if ip := rs.PrimaryIP(); ip != "" {
				gn.IP = ip
			}
		}

		switch cfg := n.Data.(type) {
		case *config.NodeConfig:
			gn.Substrate = cfg.Substrate
		case *config.RouterConfig:
			gn.Substrate = cfg.Substrate
		case *config.NetworkConfig:
			gn.CIDR = cfg.CIDR
			gn.NAT = cfg.NAT
		case *config.ActorConfig:
			gn.Extra = map[string]any{"position": cfg.Position, "port": cfg.Port}
		case *config.FirewallConfig:
			gn.Extra = map[string]any{"rules": len(cfg.Rules)}
		case *config.SSHAccessConfig:
			gn.Extra = map[string]any{"port": cfg.Port, "bind_ip": cfg.BindIP}
		case *config.ImageConfig:
			gn.Substrate = cfg.Substrate
		case *config.KernelConfig:
			gn.Substrate = cfg.Substrate
		}

		nodes = append(nodes, gn)

		// Generate edges from the graph's dependency list.
		for _, dep := range n.Deps {
			kind := "depends_on"
			switch {
			case dep.Type == "sysbox_image":
				kind = "image"
			case dep.Type == "sysbox_network":
				kind = "link"
			case dep.Type == "sysbox_kernel":
				kind = "kernel"
			case dep.Type == "sysbox_node":
				kind = "hosts"
			}

			edge := graphEdge{
				From: n.ID.String(),
				To:   dep.String(),
				Kind: kind,
			}

			// Enrich link edges with IP and interface label from config.
			if kind == "link" {
				key := n.ID.String() + "→" + dep.String()
				if infos := linkMap[key]; len(infos) > 0 {
					// If there are multiple links to the same network,
					// emit one edge per link.
					for i, info := range infos {
						e := graphEdge{
							From:  n.ID.String(),
							To:    dep.String(),
							Kind:  kind,
							IP:    info.IP,
							Label: info.Label,
						}
						if i == 0 {
							edge = e // replace the default edge
						} else {
							edges = append(edges, e)
						}
					}
				}
			}

			edges = append(edges, edge)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"topology": topology,
		"nodes":    nodes,
		"edges":    edges,
	})
}

// stripCIDRSuffix returns "10.0.1.10" given "10.0.1.10/24".
func stripCIDRSuffix(ip string) string {
	if i := strings.IndexByte(ip, '/'); i >= 0 {
		return ip[:i]
	}
	return ip
}

// ensure graph package import is used (helps go-imports keep it).
var _ = graph.New
