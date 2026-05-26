import type { GraphEdge, GraphNode } from "@/types/api"

type Props = {
  nodes: GraphNode[]
  edges: GraphEdge[]
}

export function TopologyGraph({ nodes, edges }: Props) {
  const networks = nodes.filter((node) => node.type === "sysbox_network")
  const others = nodes.filter((node) => node.type !== "sysbox_network")

  return (
    <div className="min-h-72 rounded-lg border bg-muted/30 p-4">
      <div className="grid gap-4 lg:grid-cols-[minmax(180px,240px)_1fr]">
        <div className="flex flex-col gap-3">
          {networks.length === 0 ? (
            <div className="rounded-md border bg-background p-3 text-sm text-muted-foreground">No networks</div>
          ) : (
            networks.map((network) => (
              <div key={network.id} className="rounded-md border bg-background p-3 shadow-sm">
                <div className="flex items-center justify-between gap-3">
                  <div className="truncate text-sm font-medium">{network.label}</div>
                  <div className="text-xs text-muted-foreground">{network.nat ? "nat" : "routed"}</div>
                </div>
                <div className="mt-1 truncate font-mono text-xs text-muted-foreground">{network.cidr || network.id}</div>
              </div>
            ))
          )}
        </div>
        <div className="grid auto-rows-fr gap-3 md:grid-cols-2 xl:grid-cols-3">
          {others.map((node) => {
            const links = edges.filter((edge) => edge.from === node.id || edge.to === node.id)
            return (
              <div key={node.id} className="min-h-28 rounded-md border bg-background p-3 shadow-sm">
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="truncate text-sm font-medium">{node.label}</div>
                    <div className="truncate text-xs text-muted-foreground">{node.type}</div>
                  </div>
                  <div className="rounded-md bg-secondary px-2 py-0.5 text-xs text-secondary-foreground">{node.status}</div>
                </div>
                <div className="mt-3 flex flex-col gap-1 text-xs text-muted-foreground">
                  {node.substrate ? <div className="truncate">substrate: {node.substrate}</div> : null}
                  {node.ip ? <div className="truncate font-mono">ip: {node.ip}</div> : null}
                  {links.slice(0, 3).map((edge) => (
                    <div key={`${edge.from}-${edge.to}-${edge.kind}-${edge.ip || ""}`} className="truncate">
                      {edge.kind}: {edge.ip || edge.to.replace("sysbox_network.", "")}
                    </div>
                  ))}
                </div>
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}

