import { useEffect, useMemo, useState } from "react"
import type { ELK, ElkExtendedEdge, ElkNode } from "elkjs/lib/elk-api"
import { Background, Controls, Handle, MarkerType, Position, ReactFlow, type Edge, type Node, type NodeProps } from "@xyflow/react"
import "@xyflow/react/dist/style.css"
import { Box, Cloud, Network, Router, Server } from "lucide-react"

import { cn } from "@/lib/utils"
import type { GraphEdge, GraphNode } from "@/types/api"

type Props = {
  nodes: GraphNode[]
  edges: GraphEdge[]
  onSelectNode?: (node: GraphNode) => void
}

type LayoutState = {
  nodes: Node[]
  edges: Edge[]
  loading: boolean
}

type NetworkAttachment = {
  network: GraphNode
  ip?: string
}

type FlowNodeData = {
  node: GraphNode
  attachments: NetworkAttachment[]
}

const nodeWidth = 188
const nodeHeight = 124
let elkPromise: Promise<ELK> | undefined

const nodeTypes = {
  resource: ResourceFlowNode,
}

export function TopologyGraph({ nodes, edges, onSelectNode }: Props) {
  const graphByID = useMemo(() => new Map(nodes.map((node) => [node.id, node])), [nodes])
  const view = useMemo(() => buildTopologyView(nodes, edges), [nodes, edges])
  const [flow, setFlow] = useState<LayoutState>(() => ({ ...fallbackLayout(view.nodes, view.edges, view.attachments), loading: false }))

  useEffect(() => {
    let cancelled = false
    setFlow({ ...fallbackLayout(view.nodes, view.edges, view.attachments), loading: view.nodes.length > 0 })
    layoutWithElk(view.nodes, view.edges, view.attachments)
      .then((layout) => {
        if (!cancelled) setFlow({ ...layout, loading: false })
      })
      .catch(() => {
        if (!cancelled) setFlow({ ...fallbackLayout(view.nodes, view.edges, view.attachments), loading: false })
      })
    return () => {
      cancelled = true
    }
  }, [view])

  return (
    <div className="relative h-[calc(100vh-8rem)] min-h-[680px] overflow-hidden bg-background">
      {flow.loading ? (
        <div className="absolute left-4 top-4 rounded-md border bg-card px-3 py-2 text-xs text-muted-foreground shadow-sm">
          Arranging topology
        </div>
      ) : null}
      <ReactFlow
        nodes={flow.nodes}
        edges={flow.edges}
        nodeTypes={nodeTypes}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        minZoom={0.25}
        maxZoom={1.5}
        nodesDraggable
        nodesConnectable={false}
        elementsSelectable
        onNodeClick={(_, node) => {
          const graphNode = graphByID.get(node.id)
          if (graphNode) onSelectNode?.(graphNode)
        }}
      >
        <Background gap={24} color="hsl(var(--border))" />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  )
}

function buildTopologyView(graphNodes: GraphNode[], graphEdges: GraphEdge[]) {
  const attachments = networkAttachments(graphNodes, graphEdges)
  const visible = graphNodes.filter((node) => !isArtifact(node) && node.type !== "sysbox_network")
  const visibleIDs = new Set(visible.map((node) => node.id))
  return {
    nodes: visible,
    edges: graphEdges.filter((edge) => visibleIDs.has(edge.from) && visibleIDs.has(edge.to) && edge.kind !== "image"),
    attachments,
  }
}

async function layoutWithElk(
  graphNodes: GraphNode[],
  graphEdges: GraphEdge[],
  attachments: Map<string, NetworkAttachment[]>,
): Promise<{ nodes: Node[]; edges: Edge[] }> {
  const elk = await getElk()
  const edgeModels = graphEdges.map(normalizeEdge)
  const graph: ElkNode = {
    id: "root",
    layoutOptions: {
      "elk.algorithm": "layered",
      "elk.direction": "RIGHT",
      "elk.edgeRouting": "ORTHOGONAL",
      "elk.layered.spacing.nodeNodeBetweenLayers": "120",
      "elk.spacing.nodeNode": "58",
      "elk.spacing.edgeNode": "40",
      "elk.layered.nodePlacement.strategy": "NETWORK_SIMPLEX",
      "elk.layered.crossingMinimization.strategy": "LAYER_SWEEP",
      "elk.layered.considerModelOrder.strategy": "NODES_AND_EDGES",
    },
    children: graphNodes.map((node) => ({
      id: node.id,
      width: nodeWidth,
      height: nodeHeight,
      layoutOptions: {
        "elk.layered.layering.layerConstraint": layerConstraint(node),
      },
    })),
    edges: edgeModels.map(
      (edge, index): ElkExtendedEdge => ({
        id: edgeID(edge.original, index),
        sources: [edge.source],
        targets: [edge.target],
      }),
    ),
  }

  const laidOut = await elk.layout(graph)
  const positions = new Map<string, { x: number; y: number }>(
    (laidOut.children || []).map((node) => [node.id, { x: node.x || 0, y: node.y || 0 }]),
  )
  return {
    nodes: graphNodes.map((node) => ({
      id: node.id,
      type: "resource",
      position: positions.get(node.id) || { x: 0, y: 0 },
      data: { node, attachments: attachments.get(node.id) || [] },
      style: { width: nodeWidth, height: nodeHeight },
      sourcePosition: Position.Right,
      targetPosition: Position.Left,
    })),
    edges: edgeModels.map((edge, index) => toFlowEdge(edge, index, positions)),
  }
}

async function getElk() {
  elkPromise ||= import("elkjs/lib/elk.bundled.js").then((module) => {
    const ELKConstructor = module.default as unknown as { new (): ELK }
    return new ELKConstructor()
  })
  return elkPromise
}

function fallbackLayout(
  graphNodes: GraphNode[],
  graphEdges: GraphEdge[],
  attachments: Map<string, NetworkAttachment[]>,
): { nodes: Node[]; edges: Edge[] } {
  const rowsByColumn = new Map<string, number>()
  const positioned = new Map<string, { x: number; y: number }>()
  const flowNodes = graphNodes.map((node) => {
    const column = resourceColumn(node)
    const row = rowsByColumn.get(column.key) || 0
    rowsByColumn.set(column.key, row + 1)
    const position = { x: column.x, y: 80 + row * 136 }
    positioned.set(node.id, position)
    return {
      id: node.id,
      type: "resource",
      position,
      data: { node, attachments: attachments.get(node.id) || [] },
      style: { width: nodeWidth, height: nodeHeight },
      sourcePosition: Position.Right,
      targetPosition: Position.Left,
    }
  })
  const edgeModels = graphEdges.map((edge) => {
    const normalized = normalizeEdge(edge)
    const sourcePosition = positioned.get(normalized.source)
    const targetPosition = positioned.get(normalized.target)
    if (sourcePosition && targetPosition && sourcePosition.x > targetPosition.x) {
      return { ...normalized, source: normalized.target, target: normalized.source }
    }
    return normalized
  })
  return {
    nodes: flowNodes,
    edges: edgeModels.map((edge, index) => toFlowEdge(edge, index, positioned)),
  }
}

function networkAttachments(graphNodes: GraphNode[], graphEdges: GraphEdge[]) {
  const byID = new Map(graphNodes.map((node) => [node.id, node]))
  const out = new Map<string, NetworkAttachment[]>()
  graphEdges.forEach((edge) => {
    if (edge.kind !== "link" && edge.kind !== "veth" && edge.kind !== "route") return
    const from = byID.get(edge.from)
    const to = byID.get(edge.to)
    if (!from || !to) return
    const network = from.type === "sysbox_network" ? from : to.type === "sysbox_network" ? to : undefined
    const resource = from.type === "sysbox_network" ? to : to.type === "sysbox_network" ? from : undefined
    if (!network || !resource) return
    const current = out.get(resource.id) || []
    current.push({ network, ip: edge.ip })
    out.set(resource.id, current)
  })
  return out
}

function normalizeEdge(edge: GraphEdge) {
  if (edge.kind === "image") {
    return { original: edge, source: edge.to, target: edge.from }
  }
  if (edge.kind === "link" || edge.kind === "veth") {
    return { original: edge, source: edge.to, target: edge.from }
  }
  return { original: edge, source: edge.from, target: edge.to }
}

function toFlowEdge(edge: ReturnType<typeof normalizeEdge>, index: number, positions: Map<string, { x: number; y: number }>): Edge {
  const networkEdge = edge.original.kind === "link" || edge.original.kind === "veth" || edge.original.kind === "route"
  const handles = edgeHandles(edge, positions)
  return {
    id: edgeID(edge.original, index),
    source: edge.source,
    target: edge.target,
    sourceHandle: handles.source,
    targetHandle: handles.target,
    type: "smoothstep",
    label: edge.original.label || edge.original.ip || edge.original.kind,
    animated: edge.original.kind === "route" || edge.original.kind === "veth",
    markerEnd: { type: MarkerType.ArrowClosed },
    style: {
      strokeWidth: networkEdge ? 2.4 : 1.8,
      stroke: networkEdge ? "hsl(var(--primary))" : "hsl(var(--muted-foreground))",
      strokeDasharray: networkEdge ? undefined : "5 5",
    },
    labelStyle: { fill: "hsl(var(--foreground))", fontSize: 11, fontWeight: 500 },
    labelBgPadding: [6, 3],
    labelBgBorderRadius: 4,
    labelBgStyle: { fill: "hsl(var(--background))", fillOpacity: 0.94 },
  }
}

function edgeHandles(edge: ReturnType<typeof normalizeEdge>, positions: Map<string, { x: number; y: number }>) {
  if (edge.original.kind === "image") {
    const source = positions.get(edge.source)
    const target = positions.get(edge.target)
    if (source && target && Math.abs(source.y - target.y) > nodeHeight * 0.4) {
      return source.y > target.y ? { source: "top", target: "bottom" } : { source: "bottom", target: "top" }
    }
  }
  return { source: "right", target: "left" }
}

function edgeID(edge: GraphEdge, index: number) {
  return `${edge.from}-${edge.to}-${edge.kind}-${index}`
}

function layerConstraint(node: GraphNode) {
  if (isArtifact(node)) return "FIRST"
  if (node.type === "sysbox_service") return "LAST"
  return "NONE"
}

function resourceColumn(node: GraphNode) {
  if (isArtifact(node)) return { key: "artifacts", x: 80 }
  if (node.type === "sysbox_network") return { key: "network", x: 330 }
  if (node.type === "sysbox_node" || node.type === "sysbox_router") return { key: "compute", x: 580 }
  return { key: "services", x: 830 }
}

function isArtifact(node: GraphNode) {
  return node.type === "sysbox_image" || node.type === "sysbox_kernel"
}

function ResourceFlowNode({ data, selected }: NodeProps<Node<FlowNodeData>>) {
  const node = data.node
  const attachments = data.attachments || []
  const unhealthy = node.status === "drifted" || node.status === "unknown" || node.status === "unhealthy" || node.status === "failed"
  const reason = typeof node.extra?.health_reason === "string" ? node.extra.health_reason : undefined
  return (
    <div
      className={cn(
        "relative flex h-full w-full flex-col justify-between rounded-md border bg-card p-3 text-left shadow-sm transition-colors",
        unhealthy && "border-destructive/60",
        selected ? "border-primary ring-2 ring-primary/25" : "border-border",
      )}
    >
      <Handle id="left" className="opacity-0" type="target" position={Position.Left} />
      <Handle id="right" className="opacity-0" type="source" position={Position.Right} />
      <Handle id="top" className="opacity-0" type="source" position={Position.Top} />
      <Handle id="top" className="opacity-0" type="target" position={Position.Top} />
      <Handle id="bottom" className="opacity-0" type="source" position={Position.Bottom} />
      <Handle id="bottom" className="opacity-0" type="target" position={Position.Bottom} />
      <div className="flex items-start justify-between gap-3">
        <div className="flex size-9 shrink-0 items-center justify-center rounded-md border bg-background text-muted-foreground">
          <ResourceIcon node={node} />
        </div>
        <div className="flex flex-col items-end gap-1">
          <span className="rounded-sm border bg-muted/50 px-1.5 py-0.5 font-mono text-[9px] uppercase text-muted-foreground">
            {resourceKindLabel(node)}
          </span>
          {unhealthy ? (
            <span className="rounded-sm border border-destructive/30 bg-destructive/10 px-1.5 py-0.5 font-mono text-[9px] uppercase text-destructive">
              {node.status}
            </span>
          ) : null}
        </div>
      </div>
      <div className="min-w-0">
        <div className="truncate text-sm font-semibold">{node.label}</div>
        <div className="truncate font-mono text-[10px] text-muted-foreground">
          {reason || node.ip || node.cidr || node.substrate || node.status}
        </div>
      </div>
      {attachments.length > 0 ? (
        <div className="flex min-w-0 gap-1.5">
          {attachments.slice(0, 2).map((attachment) => (
            <span
              key={attachment.network.id}
              className="min-w-0 truncate rounded-sm border bg-muted/50 px-1.5 py-0.5 font-mono text-[9px] text-muted-foreground"
              title={`${attachment.network.label} ${attachment.network.cidr || ""}`.trim()}
            >
              {attachment.network.label}
              {attachment.ip ? ` ${attachment.ip}` : ""}
            </span>
          ))}
        </div>
      ) : null}
    </div>
  )
}

function ResourceIcon({ node }: { node: GraphNode }) {
  if (node.type === "sysbox_network") return <Network />
  if (node.type === "sysbox_router") return <Router />
  if (node.type === "sysbox_node") return <Server />
  if (node.type.startsWith("sysbox_")) return <Box />
  return <Cloud />
}

function resourceKindLabel(node: GraphNode) {
  if (node.type.startsWith("sysbox_")) return node.type.replace("sysbox_", "")
  return node.type
}
