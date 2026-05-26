import { Background, Controls, MarkerType, ReactFlow, type Edge, type Node } from "@xyflow/react"
import "@xyflow/react/dist/style.css"
import { Box, Cloud, Network } from "lucide-react"

import type { GraphEdge, GraphNode } from "@/types/api"

type Props = {
  nodes: GraphNode[]
  edges: GraphEdge[]
}

export function TopologyGraph({ nodes, edges }: Props) {
  const flow = toFlow(nodes, edges)

  return (
    <div className="sysbox-panel-glow h-[680px] overflow-hidden rounded-md border bg-card">
      <div className="flex items-center justify-between border-b bg-muted/30 px-4 py-3">
        <div>
          <div className="text-sm font-medium">Topology</div>
          <div className="text-xs text-muted-foreground">{nodes.length} resources · {edges.length} links</div>
        </div>
        <div className="rounded-md border bg-background/70 px-2 py-1 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">interactive canvas</div>
      </div>
      <ReactFlow
        nodes={flow.nodes}
        edges={flow.edges}
        fitView
        minZoom={0.35}
        maxZoom={1.4}
        nodesDraggable
        nodesConnectable={false}
        elementsSelectable
      >
        <Background gap={24} color="hsl(var(--border))" />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  )
}

function toFlow(graphNodes: GraphNode[], graphEdges: GraphEdge[]): { nodes: Node[]; edges: Edge[] } {
  const networks = graphNodes.filter((node) => node.type === "sysbox_network")
  const resources = graphNodes.filter((node) => node.type !== "sysbox_network")
  const flowNodes: Node[] = []

  const networkList = networks.length > 0 ? networks : [{ id: "default", label: "default network", type: "sysbox_network", status: "unknown" } as GraphNode]
  networkList.forEach((network, index) => {
    const attached = resources.filter((node) => {
      if (networks.length === 0) return true
      return graphEdges.some((edge) => (edge.from === node.id && edge.to === network.id) || (edge.to === node.id && edge.from === network.id))
    })
    const x = 80 + index * 460
    const y = 90
    const width = 360
    const rows = Math.max(1, Math.ceil(attached.length / 2))
    const height = 180 + rows * 120

    flowNodes.push({
      id: network.id,
      type: "group",
      position: { x, y },
      style: {
        width,
        height,
        border: "2px solid hsl(var(--border))",
        borderRadius: 8,
        background: "hsl(var(--background) / 0.78)",
      },
      data: { label: "" },
    })

    flowNodes.push({
      id: `${network.id}:label`,
      position: { x: 18, y: 18 },
      parentId: network.id,
      draggable: false,
      data: { label: <NetworkLabel network={network} /> },
      style: { border: "none", background: "transparent", width: 280 },
    })

    attached.forEach((node, nodeIndex) => {
      const col = nodeIndex % 2
      const row = Math.floor(nodeIndex / 2)
      flowNodes.push({
        id: node.id,
        parentId: network.id,
        extent: "parent",
        position: { x: 42 + col * 150, y: 92 + row * 116 },
        data: { label: <ResourceNode node={node} /> },
        style: {
          width: 108,
          height: 92,
          border: "1px solid hsl(var(--border))",
          borderRadius: 8,
          background: "hsl(var(--card))",
        },
      })
    })
  })

  const linked = new Set<string>()
  graphEdges.forEach((edge) => {
    linked.add(edge.from)
    linked.add(edge.to)
  })
  const unlinked = resources.filter((node) => !linked.has(node.id))
  unlinked.forEach((node, index) => {
    flowNodes.push({
      id: node.id,
      position: { x: 100 + index * 140, y: 520 },
      data: { label: <ResourceNode node={node} /> },
      style: {
        width: 108,
        height: 92,
        border: "1px solid hsl(var(--border))",
        borderRadius: 8,
        background: "hsl(var(--card))",
      },
    })
  })

  return {
    nodes: flowNodes,
    edges: graphEdges.map((edge, index) => ({
      id: `${edge.from}-${edge.to}-${index}`,
      source: edge.from,
      target: edge.to,
      label: edge.label || edge.ip || edge.kind,
      animated: edge.kind === "route" || edge.kind === "veth",
      markerEnd: { type: MarkerType.ArrowClosed },
      style: { strokeWidth: 2, stroke: "hsl(var(--muted-foreground))" },
      labelStyle: { fill: "hsl(var(--muted-foreground))", fontSize: 11 },
    })),
  }
}

function NetworkLabel({ network }: { network: GraphNode }) {
  return (
    <div className="flex items-center gap-2 text-left">
      <div className="flex size-8 items-center justify-center rounded-md border bg-card">
        <Network />
      </div>
      <div className="min-w-0">
        <div className="truncate text-sm font-semibold">{network.label}</div>
        <div className="truncate font-mono text-[11px] text-muted-foreground">{network.cidr || network.id}</div>
      </div>
    </div>
  )
}

function ResourceNode({ node }: { node: GraphNode }) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-2 p-2 text-center">
      <div className="flex size-9 items-center justify-center rounded-md border bg-background">
        {node.type === "sysbox_node" ? <Box /> : <Cloud />}
      </div>
      <div className="max-w-24 truncate text-xs font-medium">{node.label}</div>
      <div className="max-w-24 truncate font-mono text-[10px] text-muted-foreground">{node.ip || node.substrate || node.status}</div>
    </div>
  )
}
