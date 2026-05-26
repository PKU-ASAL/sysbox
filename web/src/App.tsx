import { useCallback, useEffect, useMemo, useState } from "react"
import {
  Activity,
  Box,
  Braces,
  Cable,
  CheckCircle2,
  Cloud,
  Container,
  FileCode2,
  GitBranch,
  LayoutDashboard,
  Loader2,
  Play,
  Plus,
  RefreshCw,
  Server,
  SquareTerminal,
  Trash2,
} from "lucide-react"

import { ConsoleDialog } from "@/components/ConsoleDialog"
import { StatusBadge } from "@/components/StatusBadge"
import { TopologyGraph } from "@/components/TopologyGraph"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Separator } from "@/components/ui/separator"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import { usePolling } from "@/hooks/usePolling"
import { api, getToken, setToken } from "@/lib/api"
import type { Agent, GraphEdge, GraphNode, NodeInfo, OutputValue, Plan, ResourceHealth, Run, Topology, TopologyHealth } from "@/types/api"

const starterHcl = `substrate "docker" {
  alias = "local"
}

resource "sysbox_network" "app" {
  cidr = "172.31.20.0/24"
  nat  = true
}

resource "sysbox_image" "nginx" {
  substrate  = substrate.docker.local
  docker_ref = "nginx:alpine"
}

resource "sysbox_node" "web" {
  substrate = substrate.docker.local
  image     = sysbox_image.nginx.id

  link {
    network = sysbox_network.app.id
    ip      = "172.31.20.10/24"
  }
}

output "web_ip" {
  value = "172.31.20.10"
}
`

type Detail = {
  hcl?: string
  plan?: Plan
  plans?: Plan[]
  runs?: Run[]
  outputs?: Record<string, OutputValue>
  health?: TopologyHealth
  resources?: ResourceHealth[]
  nodes?: NodeInfo[]
  graph?: { nodes: GraphNode[]; edges: GraphEdge[] }
}

export default function App() {
  const [selected, setSelected] = useState<string>("")
  const [detail, setDetail] = useState<Detail>({})
  const [notice, setNotice] = useState("")
  const [busy, setBusy] = useState("")
  const [tokenValue, setTokenValue] = useState(getToken())
  const [createOpen, setCreateOpen] = useState(false)
  const [newName, setNewName] = useState("docker-service")
  const [newHcl, setNewHcl] = useState(starterHcl)
  const [consoleNode, setConsoleNode] = useState<string | undefined>()

  const overview = usePolling(
    async () => {
      const [health, agents, topologies, runs] = await Promise.all([api.health(), api.agents(), api.topologies(), api.runs()])
      return { health, agents: agents.agents, topologies: topologies.topologies, runs: runs.runs }
    },
    4000,
  )

  const topologies = overview.data?.topologies || []
  const agents = overview.data?.agents || []
  const runs = overview.data?.runs || []

  useEffect(() => {
    if (!selected && topologies.length > 0) {
      setSelected(topologies[0].name)
    }
  }, [selected, topologies])

  const selectedTopology = useMemo(() => topologies.find((topology) => topology.name === selected), [selected, topologies])

  const refreshDetail = useCallback(async () => {
    if (!selected) {
      setDetail({})
      return
    }
    const result: Detail = {}
    const tasks = await Promise.allSettled([
      api.getHcl(selected),
      api.plans(selected),
      api.outputs(selected),
      api.healthOfTopology(selected),
      api.resources(selected),
      api.nodes(selected),
      api.graph(selected),
    ])
    if (tasks[0].status === "fulfilled") result.hcl = tasks[0].value
    if (tasks[1].status === "fulfilled") result.plans = tasks[1].value.plans
    if (tasks[2].status === "fulfilled") result.outputs = tasks[2].value.outputs
    if (tasks[3].status === "fulfilled") result.health = tasks[3].value
    if (tasks[4].status === "fulfilled") result.resources = tasks[4].value.resources
    if (tasks[5].status === "fulfilled") result.nodes = tasks[5].value.nodes
    if (tasks[6].status === "fulfilled") result.graph = { nodes: tasks[6].value.nodes, edges: tasks[6].value.edges }
    result.runs = runs.filter((run) => run.topology === selected || run.workspace === selected).slice(0, 8)
    setDetail(result)
  }, [runs, selected])

  useEffect(() => {
    void refreshDetail()
  }, [refreshDetail])

  async function mutate(label: string, fn: () => Promise<unknown>) {
    setBusy(label)
    setNotice("")
    try {
      await fn()
      await overview.refresh()
      await refreshDetail()
      setNotice(`${label} completed`)
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy("")
    }
  }

  async function createTopology() {
    await mutate("create topology", async () => {
      await api.createTopology(newName, newHcl)
      setSelected(newName)
      setCreateOpen(false)
    })
  }

  async function saveHcl() {
    if (!selected || detail.hcl === undefined) return
    await mutate("save HCL", () => api.updateHcl(selected, detail.hcl || ""))
  }

  async function createPlan() {
    if (!selected) return
    await mutate("create plan", async () => {
      const plan = await api.createPlan(selected)
      setDetail((prev) => ({ ...prev, plan }))
    })
  }

  async function applyPlan() {
    if (!selected) return
    await mutate("apply", async () => {
      const planID = detail.plan?.id || detail.plans?.[0]?.id
      const run = await api.apply(selected, planID)
      await waitRun(run.run_id)
    })
  }

  async function destroyTopology() {
    if (!selected) return
    await mutate("destroy", async () => {
      const run = await api.destroy(selected)
      await waitRun(run.run_id)
    })
  }

  async function deleteTopology() {
    if (!selected) return
    const name = selected
    await mutate("delete topology", async () => {
      await api.deleteTopology(name)
      setSelected("")
    })
  }

  async function waitRun(id: string) {
    for (let i = 0; i < 180; i++) {
      const run = await api.run(id)
      if (run.status === "done") return
      if (run.status === "failed" || run.status === "cancelled") {
        throw new Error(run.error || `run ${run.status}`)
      }
      await new Promise((resolve) => window.setTimeout(resolve, 1000))
    }
    throw new Error("run timed out")
  }

  function saveToken() {
    setToken(tokenValue)
    void overview.refresh()
    void refreshDetail()
  }

  return (
    <div className="min-h-screen bg-background">
      <aside className="fixed inset-y-0 left-0 hidden w-64 border-r bg-muted/20 lg:block">
        <div className="flex h-full flex-col">
          <div className="flex h-16 items-center gap-3 px-5">
            <div className="flex size-9 items-center justify-center rounded-md bg-primary text-primary-foreground">
              <Box />
            </div>
            <div>
              <div className="font-semibold">sysbox</div>
              <div className="text-xs text-muted-foreground">Control plane</div>
            </div>
          </div>
          <nav className="flex flex-1 flex-col gap-1 px-3 py-3">
            <NavItem icon={LayoutDashboard} active label="Workspaces" />
            <NavItem icon={Server} label="Agents" />
            <NavItem icon={Activity} label="Runs" />
            <NavItem icon={Braces} label="Artifacts" />
          </nav>
          <div className="p-4">
            <div className="rounded-lg border bg-background p-3">
              <div className="flex items-center justify-between gap-3">
                <span className="text-sm font-medium">API</span>
                <StatusBadge status={overview.data?.health.status || (overview.error ? "offline" : "checking")} />
              </div>
              <div className="mt-2 text-xs text-muted-foreground">/v1 via same-origin proxy</div>
            </div>
          </div>
        </div>
      </aside>

      <main className="lg:pl-64">
        <header className="sticky top-0 z-10 border-b bg-background/95 backdrop-blur">
          <div className="flex min-h-16 flex-wrap items-center justify-between gap-3 px-4 py-3 lg:px-6">
            <div>
              <h1 className="text-xl font-semibold tracking-normal">Workspaces</h1>
              <p className="text-sm text-muted-foreground">Plan, apply, observe, and connect to sysbox topologies.</p>
            </div>
            <div className="flex items-center gap-2">
              <Input className="w-48" placeholder="API token" value={tokenValue} onChange={(event) => setTokenValue(event.target.value)} />
              <Button variant="outline" onClick={saveToken}>
                Save
              </Button>
              <Button variant="outline" size="icon" onClick={() => void overview.refresh()} aria-label="Refresh">
                <RefreshCw />
              </Button>
              <Dialog open={createOpen} onOpenChange={setCreateOpen}>
                <DialogTrigger asChild>
                  <Button>
                    <Plus data-icon="inline-start" />
                    New workspace
                  </Button>
                </DialogTrigger>
                <DialogContent className="max-w-3xl">
                  <DialogHeader>
                    <DialogTitle>Create workspace</DialogTitle>
                    <DialogDescription>Upload HCL and start from a plan.</DialogDescription>
                  </DialogHeader>
                  <div className="flex flex-col gap-4">
                    <div className="flex flex-col gap-2">
                      <Label htmlFor="workspace-name">Name</Label>
                      <Input id="workspace-name" value={newName} onChange={(event) => setNewName(event.target.value)} />
                    </div>
                    <div className="flex flex-col gap-2">
                      <Label htmlFor="workspace-hcl">HCL</Label>
                      <Textarea id="workspace-hcl" className="min-h-80 font-mono" value={newHcl} onChange={(event) => setNewHcl(event.target.value)} />
                    </div>
                  </div>
                  <DialogFooter>
                    <Button variant="outline" onClick={() => setCreateOpen(false)}>
                      Cancel
                    </Button>
                    <Button onClick={createTopology} disabled={busy !== ""}>
                      Create
                    </Button>
                  </DialogFooter>
                </DialogContent>
              </Dialog>
            </div>
          </div>
        </header>

        <div className="grid gap-6 p-4 lg:grid-cols-[320px_1fr] lg:p-6">
          <section className="flex flex-col gap-4">
            <MetricGrid topologies={topologies} agents={agents} runs={runs} />
            <Card>
              <CardHeader>
                <CardTitle>Workspaces</CardTitle>
                <CardDescription>{topologies.length} topology configuration units</CardDescription>
              </CardHeader>
              <CardContent className="flex flex-col gap-2">
                {topologies.length === 0 ? (
                  <EmptyLine text="No workspaces yet" />
                ) : (
                  topologies.map((topology) => (
                    <button
                      key={topology.name}
                      className={`rounded-md border p-3 text-left transition-colors hover:bg-muted/60 ${selected === topology.name ? "border-primary bg-muted" : "bg-background"}`}
                      onClick={() => setSelected(topology.name)}
                    >
                      <div className="flex items-center justify-between gap-3">
                        <div className="min-w-0">
                          <div className="truncate text-sm font-medium">{topology.name}</div>
                          <div className="truncate text-xs text-muted-foreground">
                            serial {topology.serial || 0} · {topology.resource_count || 0} resources
                          </div>
                        </div>
                        <StatusBadge status={topology.has_state ? "applied" : "new"} />
                      </div>
                    </button>
                  ))
                )}
              </CardContent>
            </Card>
            <AgentsCard agents={agents} />
          </section>

          <section className="min-w-0">
            {!selectedTopology ? (
              <Card>
                <CardHeader>
                  <CardTitle>No workspace selected</CardTitle>
                  <CardDescription>Create or select a workspace to begin.</CardDescription>
                </CardHeader>
              </Card>
            ) : (
              <div className="flex flex-col gap-4">
                <Card>
                  <CardHeader>
                    <div className="flex flex-wrap items-start justify-between gap-3">
                      <div>
                        <CardTitle>{selectedTopology.name}</CardTitle>
                        <CardDescription>
                          {selectedTopology.backend || "local"} · serial {selectedTopology.serial || 0}
                        </CardDescription>
                      </div>
                      <div className="flex flex-wrap items-center gap-2">
                        <Button variant="outline" onClick={createPlan} disabled={busy !== ""}>
                          <GitBranch data-icon="inline-start" />
                          Plan
                        </Button>
                        <Button onClick={applyPlan} disabled={busy !== ""}>
                          <Play data-icon="inline-start" />
                          Apply
                        </Button>
                        <Button variant="outline" onClick={destroyTopology} disabled={busy !== ""}>
                          Destroy
                        </Button>
                        <Button variant="destructive" size="icon" onClick={deleteTopology} disabled={busy !== ""} aria-label="Delete">
                          <Trash2 />
                        </Button>
                      </div>
                    </div>
                    {notice ? <div className="rounded-md border bg-muted px-3 py-2 text-sm">{notice}</div> : null}
                    {busy ? (
                      <div className="flex items-center gap-2 text-sm text-muted-foreground">
                        <Loader2 className="animate-spin" />
                        {busy}
                      </div>
                    ) : null}
                  </CardHeader>
                  <CardContent>
                    <Tabs defaultValue="overview">
                      <TabsList>
                        <TabsTrigger value="overview">Overview</TabsTrigger>
                        <TabsTrigger value="plan">Plan</TabsTrigger>
                        <TabsTrigger value="resources">Resources</TabsTrigger>
                        <TabsTrigger value="hcl">HCL</TabsTrigger>
                        <TabsTrigger value="runs">Runs</TabsTrigger>
                      </TabsList>

                      <TabsContent value="overview">
                        <div className="grid gap-4 xl:grid-cols-[1fr_320px]">
                          <TopologyGraph nodes={detail.graph?.nodes || []} edges={detail.graph?.edges || []} />
                          <div className="flex flex-col gap-4">
                            <SummaryCard title="Health" value={detail.health?.status || "unknown"} icon={CheckCircle2} />
                            <OutputsCard outputs={detail.outputs || {}} />
                            <NodesCard topology={selectedTopology.name} nodes={detail.nodes || []} onConsole={setConsoleNode} />
                          </div>
                        </div>
                      </TabsContent>

                      <TabsContent value="plan">
                        <PlanView plan={detail.plan || detail.plans?.[0]} />
                      </TabsContent>

                      <TabsContent value="resources">
                        <ResourcesTable resources={detail.resources || []} />
                      </TabsContent>

                      <TabsContent value="hcl">
                        <div className="flex flex-col gap-3">
                          <Textarea className="min-h-[520px] font-mono" value={detail.hcl || ""} onChange={(event) => setDetail((prev) => ({ ...prev, hcl: event.target.value }))} />
                          <div className="flex justify-end">
                            <Button onClick={saveHcl} disabled={busy !== ""}>
                              <FileCode2 data-icon="inline-start" />
                              Save HCL
                            </Button>
                          </div>
                        </div>
                      </TabsContent>

                      <TabsContent value="runs">
                        <RunsTable runs={detail.runs || []} />
                      </TabsContent>
                    </Tabs>
                  </CardContent>
                </Card>
              </div>
            )}
          </section>
        </div>
      </main>

      <ConsoleDialog topology={selected} node={consoleNode} open={Boolean(consoleNode)} onOpenChange={(open) => !open && setConsoleNode(undefined)} />
    </div>
  )
}

function NavItem({ icon: Icon, label, active }: { icon: typeof LayoutDashboard; label: string; active?: boolean }) {
  return (
    <div className={`flex items-center gap-3 rounded-md px-3 py-2 text-sm ${active ? "bg-background font-medium shadow-sm" : "text-muted-foreground"}`}>
      <Icon />
      {label}
    </div>
  )
}

function MetricGrid({ topologies, agents, runs }: { topologies: Topology[]; agents: Agent[]; runs: Run[] }) {
  return (
    <div className="grid grid-cols-3 gap-2">
      <MiniMetric label="Workspaces" value={topologies.length} />
      <MiniMetric label="Agents" value={agents.filter((agent) => agent.status === "online").length} />
      <MiniMetric label="Runs" value={runs.filter((run) => run.status === "running" || run.status === "queued" || run.status === "assigned").length} />
    </div>
  )
}

function MiniMetric({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-lg border bg-card p-3">
      <div className="text-lg font-semibold">{value}</div>
      <div className="truncate text-xs text-muted-foreground">{label}</div>
    </div>
  )
}

function AgentsCard({ agents }: { agents: Agent[] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Agents</CardTitle>
        <CardDescription>Host executors connected to the control plane</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {agents.length === 0 ? (
          <EmptyLine text="No agents registered" />
        ) : (
          agents.map((agent) => (
            <div key={agent.id} className="rounded-md border p-3">
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <div className="truncate text-sm font-medium">{agent.name || agent.id}</div>
                  <div className="truncate text-xs text-muted-foreground">{agent.capabilities?.join(", ") || "no capabilities"}</div>
                </div>
                <StatusBadge status={agent.disabled ? "disabled" : agent.quarantined ? "quarantined" : agent.status} />
              </div>
            </div>
          ))
        )}
      </CardContent>
    </Card>
  )
}

function SummaryCard({ title, value, icon: Icon }: { title: string; value: string; icon: typeof Cloud }) {
  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <div>
          <CardTitle>{title}</CardTitle>
          <CardDescription>Latest observation</CardDescription>
        </div>
        <Icon />
      </CardHeader>
      <CardContent>
        <StatusBadge status={value} />
      </CardContent>
    </Card>
  )
}

function OutputsCard({ outputs }: { outputs: Record<string, OutputValue> }) {
  const entries = Object.entries(outputs)
  return (
    <Card>
      <CardHeader>
        <CardTitle>Outputs</CardTitle>
        <CardDescription>Terraform-style values</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {entries.length === 0 ? (
          <EmptyLine text="No outputs" />
        ) : (
          entries.map(([name, output]) => (
            <div key={name} className="flex items-center justify-between gap-3 rounded-md border px-3 py-2">
              <span className="text-sm font-medium">{name}</span>
              <code className="truncate text-xs text-muted-foreground">{String(output.value)}</code>
            </div>
          ))
        )}
      </CardContent>
    </Card>
  )
}

function NodesCard({ nodes, onConsole }: { topology: string; nodes: NodeInfo[]; onConsole: (node: string) => void }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Nodes</CardTitle>
        <CardDescription>Open an agent-backed console</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {nodes.length === 0 ? (
          <EmptyLine text="No nodes" />
        ) : (
          nodes.map((node) => (
            <div key={node.name} className="flex items-center justify-between gap-3 rounded-md border px-3 py-2">
              <div className="min-w-0">
                <div className="truncate text-sm font-medium">{node.name}</div>
                <div className="truncate text-xs text-muted-foreground">
                  {node.provider}
                  {node.primary_ip ? ` · ${node.primary_ip}` : ""}
                </div>
              </div>
              <Button variant="outline" size="icon" aria-label="Open console" onClick={() => onConsole(node.name)}>
                <SquareTerminal />
              </Button>
            </div>
          ))
        )}
      </CardContent>
    </Card>
  )
}

function PlanView({ plan }: { plan?: Plan }) {
  if (!plan) {
    return <EmptyLine text="Create a plan to see field-level actions." />
  }
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-md border p-3">
        <div>
          <div className="text-sm font-medium">{plan.id}</div>
          <div className="text-xs text-muted-foreground">{plan.summary || "Plan ready"}</div>
        </div>
        <StatusBadge status={plan.status} />
      </div>
      <div className="rounded-md border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Action</TableHead>
              <TableHead>Resource</TableHead>
              <TableHead>Reason</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {plan.actions.map((action, index) => (
              <TableRow key={`${action.resource}-${index}`}>
                <TableCell>
                  <Badge variant="secondary">{action.action}</Badge>
                </TableCell>
                <TableCell className="font-mono text-xs">{action.resource}</TableCell>
                <TableCell className="text-muted-foreground">{action.reason || ""}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </div>
  )
}

function ResourcesTable({ resources }: { resources: ResourceHealth[] }) {
  return (
    <div className="rounded-md border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Resource</TableHead>
            <TableHead>Provider</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Reason</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {resources.length === 0 ? (
            <TableRow>
              <TableCell colSpan={4}>
                <EmptyLine text="No resources" />
              </TableCell>
            </TableRow>
          ) : (
            resources.map((resource, index) => (
              <TableRow key={`${resource.resource || resource.name}-${index}`}>
                <TableCell className="font-mono text-xs">{resource.resource || `${resource.type}.${resource.name}`}</TableCell>
                <TableCell>{resource.provider || ""}</TableCell>
                <TableCell>
                  <StatusBadge status={resource.status} />
                </TableCell>
                <TableCell className="text-muted-foreground">{resource.reason || ""}</TableCell>
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>
    </div>
  )
}

function RunsTable({ runs }: { runs: Run[] }) {
  return (
    <div className="rounded-md border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Run</TableHead>
            <TableHead>Operation</TableHead>
            <TableHead>Agent</TableHead>
            <TableHead>Status</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {runs.length === 0 ? (
            <TableRow>
              <TableCell colSpan={4}>
                <EmptyLine text="No runs" />
              </TableCell>
            </TableRow>
          ) : (
            runs.map((run) => (
              <TableRow key={run.id}>
                <TableCell className="font-mono text-xs">{run.id}</TableCell>
                <TableCell>{run.operation || run.op}</TableCell>
                <TableCell>{run.agent_id || ""}</TableCell>
                <TableCell>
                  <StatusBadge status={run.status} />
                </TableCell>
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>
    </div>
  )
}

function EmptyLine({ text }: { text: string }) {
  return (
    <div className="flex min-h-16 items-center justify-center rounded-md border border-dashed text-sm text-muted-foreground">
      {text}
    </div>
  )
}

