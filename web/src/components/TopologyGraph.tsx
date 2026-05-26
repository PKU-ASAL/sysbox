import type { GraphEdge, GraphNode } from "@/types/api"

type Props = {
  nodes: GraphNode[]
  edges: GraphEdge[]
}

export function TopologyGraph({ nodes, edges }: Props) {
  const networks = nodes.filter((node) => node.type === "sysbox_network")
  const others = nodes.filter((node) => node.type !== "sysbox_network")

  return (
    <div className="sysbox-panel-glow min-h-[520px] overflow-hidden rounded-md border bg-card/95">
      <div className="flex items-center justify-between border-b bg-muted/35 px-4 py-3">
        <div>
          <div className="sysbox-eyebrow">iac canvas</div>
          <div className="mt-1 text-sm font-medium">Topology canvas</div>
          <div className="text-xs text-muted-foreground">{nodes.length} resources · {edges.length} links</div>
        </div>
        <div className="rounded-md border bg-background/70 px-2 py-1 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">IaC view</div>
      </div>

      <div className="relative min-h-[460px] p-4">
        <div className="absolute inset-0 bg-[linear-gradient(to_right,hsl(var(--border))_1px,transparent_1px),linear-gradient(to_bottom,hsl(var(--border))_1px,transparent_1px)] bg-[size:24px_24px] opacity-25" />
        <div className="absolute left-8 top-8 h-24 w-24 rounded-full bg-primary/10 blur-3xl" />
        <div className="relative grid gap-4">
          {networks.length === 0 ? (
            <CanvasLane network={{ id: "default", label: "default network", type: "sysbox_network", status: "unknown" }} nodes={others} edges={edges} />
          ) : (
            networks.map((network) => {
              const laneNodes = others.filter((node) => edges.some((edge) => (edge.from === node.id && edge.to === network.id) || (edge.to === node.id && edge.from === network.id)))
              return <CanvasLane key={network.id} network={network} nodes={laneNodes} edges={edges} />
            })
          )}
          {networks.length > 0 && others.some((node) => !edges.some((edge) => edge.from === node.id || edge.to === node.id)) ? (
            <CanvasLane network={{ id: "unlinked", label: "unlinked resources", type: "sysbox_network", status: "unknown" }} nodes={others.filter((node) => !edges.some((edge) => edge.from === node.id || edge.to === node.id))} edges={edges} />
          ) : null}
        </div>
      </div>
    </div>
  )
}

function CanvasLane({ network, nodes, edges }: { network: GraphNode; nodes: GraphNode[]; edges: GraphEdge[] }) {
  return (
    <section className="rounded-md border bg-background/80 shadow-sm">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b px-4 py-3">
        <div className="min-w-0">
          <div className="truncate text-sm font-medium">{network.label}</div>
          <div className="truncate font-mono text-xs text-muted-foreground">{network.cidr || network.id}</div>
        </div>
        <div className="rounded-md border px-2 py-1 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">{network.nat ? "nat" : "routed"}</div>
      </div>

      <div className="grid gap-3 p-4 md:grid-cols-2 xl:grid-cols-3">
        {nodes.length === 0 ? (
          <div className="rounded-md border border-dashed bg-background/80 p-4 text-sm text-muted-foreground">No attached resources</div>
        ) : (
          nodes.map((node) => <CanvasNode key={`${network.id}-${node.id}`} node={node} links={edges.filter((edge) => edge.from === node.id || edge.to === node.id)} />)
        )}
      </div>
    </section>
  )
}

function CanvasNode({ node, links }: { node: GraphNode; links: GraphEdge[] }) {
  return (
    <div className="rounded-md border bg-card p-3 shadow-sm">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="truncate text-sm font-medium">{node.label}</div>
          <div className="truncate text-xs text-muted-foreground">{node.type}</div>
        </div>
        <div className="rounded-md border border-primary/40 bg-primary/10 px-2 py-0.5 font-mono text-[10px] uppercase tracking-[0.12em] text-primary">{node.status}</div>
      </div>
      <div className="mt-3 grid gap-1 text-xs text-muted-foreground">
        {node.substrate ? <div className="truncate">substrate: {node.substrate}</div> : null}
        {node.ip ? <div className="truncate font-mono">ip: {node.ip}</div> : null}
        {links.slice(0, 3).map((edge) => (
          <div key={`${edge.from}-${edge.to}-${edge.kind}-${edge.ip || ""}`} className="truncate border-l pl-2">
            {edge.kind}: {edge.ip || edge.to.replace("sysbox_network.", "")}
          </div>
        ))}
      </div>
    </div>
  )
}
