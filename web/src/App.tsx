import { useCallback, useEffect, useMemo, useState } from "react"
import { Navigate, Route, Routes, useLocation, useNavigate, useParams } from "react-router-dom"
import {
  ArrowLeft,
  CheckCircle2,
  Cloud,
  Database,
  FileCode2,
  Filter,
  GitBranch,
  Loader2,
  Moon,
  MousePointer2,
  Play,
  Plus,
  RefreshCw,
  SquareTerminal,
  Sun,
  Trash2,
} from "lucide-react"

import { AppSidebar, type AppPage } from "@/components/app-sidebar"
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
import {
  SidebarInset,
  SidebarProvider,
  SidebarTrigger,
} from "@/components/ui/sidebar"
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
  outputs?: Record<string, OutputValue>
  health?: TopologyHealth
  resources?: ResourceHealth[]
  nodes?: NodeInfo[]
  graph?: { nodes: GraphNode[]; edges: GraphEdge[] }
}

export default function App() {
  const location = useLocation()
  const navigate = useNavigate()
  const [detail, setDetail] = useState<Detail>({})
  const [notice, setNotice] = useState("")
  const [busy, setBusy] = useState("")
  const [tokenValue, setTokenValue] = useState(getToken())
  const [createOpen, setCreateOpen] = useState(false)
  const [newName, setNewName] = useState("docker-service")
  const [newHcl, setNewHcl] = useState(starterHcl)
  const [consoleNode, setConsoleNode] = useState<string | undefined>()
  const [theme, setTheme] = useState(() => localStorage.getItem("sysbox.theme") || "dark")

  const overview = usePolling(
    async () => {
      const [health, agents, topologies, runs] = await Promise.all([api.health(), api.agents(), api.topologies(), api.runs()])
      return { health, agents: agents.agents, topologies: topologies.topologies, runs: runs.runs }
    },
    10000,
  )

  const topologies = overview.data?.topologies || []
  const agents = overview.data?.agents || []
  const runs = overview.data?.runs || []
  const deployedTopologies = useMemo(() => topologies.filter((topology) => topology.has_state), [topologies])
  const activePage = pageFromPath(location.pathname)
  const selectedResource = selectedResourceFromPath(location.pathname)

  const refreshDetail = useCallback(async () => {
    if (!selectedResource || activePage === "agents" || activePage === "dashboard") {
      setDetail({})
      return
    }
    const result: Detail = {}
    const tasks = await Promise.allSettled([
      api.getHcl(selectedResource),
      api.plans(selectedResource),
      api.outputs(selectedResource),
      api.healthOfTopology(selectedResource),
      api.resources(selectedResource),
      api.nodes(selectedResource),
      api.graph(selectedResource),
    ])
    if (tasks[0].status === "fulfilled") result.hcl = tasks[0].value
    if (tasks[1].status === "fulfilled") result.plans = tasks[1].value.plans
    if (tasks[2].status === "fulfilled") result.outputs = tasks[2].value.outputs
    if (tasks[3].status === "fulfilled") result.health = tasks[3].value
    if (tasks[4].status === "fulfilled") result.resources = tasks[4].value.resources
    if (tasks[5].status === "fulfilled") result.nodes = tasks[5].value.nodes
    if (tasks[6].status === "fulfilled") result.graph = { nodes: tasks[6].value.nodes, edges: tasks[6].value.edges }
    setDetail(result)
  }, [activePage, selectedResource])

  useEffect(() => {
    void refreshDetail()
  }, [refreshDetail])

  useEffect(() => {
    document.documentElement.classList.toggle("light", theme === "light")
    localStorage.setItem("sysbox.theme", theme)
  }, [theme])

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
      navigate(`/artifacts/${encodeURIComponent(newName)}`)
      setCreateOpen(false)
    })
  }

  async function saveHcl() {
    if (!selectedResource || detail.hcl === undefined) return
    await mutate("save HCL", () => api.updateHcl(selectedResource, detail.hcl || ""))
  }

  async function createPlan() {
    if (!selectedResource) return
    await mutate("create plan", async () => {
      const plan = await api.createPlan(selectedResource)
      setDetail((prev) => ({ ...prev, plan }))
    })
  }

  async function applyPlan() {
    if (!selectedResource) return
    await mutate("apply", async () => {
      const planID = detail.plan?.id || detail.plans?.[0]?.id
      const run = await api.apply(selectedResource, planID)
      await waitRun(run.run_id)
    })
  }

  async function destroyTopology() {
    if (!selectedResource) return
    await mutate("destroy", async () => {
      const run = await api.destroy(selectedResource)
      await waitRun(run.run_id)
    })
  }

  async function deleteTopology() {
    if (!selectedResource) return
    const name = selectedResource
    await mutate("delete topology", async () => {
      await api.deleteTopology(name)
      navigate("/artifacts")
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
  const pageTitle = titleFromPath(location.pathname)
  const pageDescription = descriptionFromPage(activePage)

  return (
    <SidebarProvider>
      <AppSidebar
        activePage={activePage}
        apiStatus={overview.data?.health.status || (overview.error ? "offline" : "checking")}
        agents={agents}
        runs={runs}
        topologies={topologies}
      />
      <SidebarInset>
        <header className="sticky top-0 z-10 flex min-h-16 shrink-0 items-center gap-3 border-b bg-background/85 px-4 py-3 backdrop-blur lg:px-6">
          <SidebarTrigger className="-ml-1" />
          <div className="flex min-w-0 flex-1 flex-wrap items-center justify-between gap-3">
            <div>
              <div className="sysbox-eyebrow">sysbox console</div>
              <h1 className="mt-1 text-lg font-semibold tracking-normal">{pageTitle}</h1>
              <p className="text-xs text-muted-foreground">{pageDescription}</p>
            </div>
            <div className="flex items-center gap-2">
              <Input className="w-48" placeholder="API token" value={tokenValue} onChange={(event) => setTokenValue(event.target.value)} />
              <Button variant="outline" onClick={saveToken}>
                Save
              </Button>
              <Button variant="outline" size="icon" onClick={() => void overview.refresh()} aria-label="Refresh">
                <RefreshCw />
              </Button>
              <Button
                variant="outline"
                size="icon"
                onClick={() => setTheme((current) => (current === "dark" ? "light" : "dark"))}
                aria-label="Toggle theme"
              >
                {theme === "dark" ? <Sun /> : <Moon />}
              </Button>
              {activePage === "artifacts" ? (
                <Dialog open={createOpen} onOpenChange={setCreateOpen}>
                  <DialogTrigger asChild>
                    <Button>
                      <Plus data-icon="inline-start" />
                      New HCL
                    </Button>
                  </DialogTrigger>
                  <DialogContent className="max-w-3xl">
                    <DialogHeader>
                      <DialogTitle>Create HCL artifact</DialogTitle>
                      <DialogDescription>Save HCL, create a plan, then apply it.</DialogDescription>
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
              ) : null}
            </div>
          </div>
        </header>

        <div className="p-4 lg:p-6">
          {notice ? <div className="mb-4 rounded-md border bg-muted/70 px-3 py-2 text-xs">{notice}</div> : null}
          {busy ? (
            <div className="mb-4 flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="animate-spin" />
              {busy}
            </div>
          ) : null}

          <Routes>
            <Route path="/" element={<DashboardPage topologies={topologies} deployedTopologies={deployedTopologies} agents={agents} runs={runs} apiStatus={overview.data?.health.status || (overview.error ? "offline" : "checking")} />} />
            <Route path="/agents" element={<AgentsListPage agents={agents} />} />
            <Route path="/agents/:agentId" element={<AgentDetailRoute agents={agents} runs={runs} />} />
            <Route path="/artifacts" element={<ArtifactsListPage topologies={topologies} />} />
            <Route path="/artifacts/:artifactId" element={<ArtifactDetailRoute topologies={topologies} runs={runs} detail={detail} busy={busy} onCreatePlan={createPlan} onApplyPlan={applyPlan} onDestroy={destroyTopology} onDelete={deleteTopology} onSaveHcl={saveHcl} onHclChange={(hcl) => setDetail((prev) => ({ ...prev, hcl }))} />} />
            <Route path="/topologies" element={<TopologiesListPage topologies={deployedTopologies} />} />
            <Route path="/topologies/:topologyId" element={<TopologyDetailRoute topologies={deployedTopologies} detail={detail} onConsole={setConsoleNode} />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </div>
      </SidebarInset>

      <ConsoleDialog topology={selectedResource || ""} node={consoleNode} open={Boolean(consoleNode)} onOpenChange={(open) => !open && setConsoleNode(undefined)} />
    </SidebarProvider>
  )
}

function pageFromPath(pathname: string): AppPage {
  if (pathname.startsWith("/agents")) return "agents"
  if (pathname.startsWith("/artifacts")) return "artifacts"
  if (pathname.startsWith("/topologies")) return "topologies"
  return "dashboard"
}

function selectedResourceFromPath(pathname: string) {
  const segments = pathname.split("/").filter(Boolean)
  if ((segments[0] === "artifacts" || segments[0] === "topologies") && segments[1]) {
    return decodeURIComponent(segments[1])
  }
  return ""
}

function titleFromPath(pathname: string) {
  const segments = pathname.split("/").filter(Boolean)
  if (segments.length >= 2) return `${segments[0]}/${decodeURIComponent(segments[1])}`
  if (segments[0] === "agents") return "Agents"
  if (segments[0] === "artifacts") return "Artifacts"
  if (segments[0] === "topologies") return "Topologies"
  return "Dashboard"
}

function descriptionFromPage(page: AppPage) {
  return {
    dashboard: "A compact overview of API, agents, topologies, and runs.",
    agents: "Registered executors connected to the control plane.",
    artifacts: "HCL configuration artifacts. Create, review, plan, and apply revisions.",
    topologies: "Deployed topology environments and their live resources.",
  }[page]
}

function DashboardPage({
  topologies,
  deployedTopologies,
  agents,
  runs,
  apiStatus,
}: {
  topologies: Topology[]
  deployedTopologies: Topology[]
  agents: Agent[]
  runs: Run[]
  apiStatus: string
}) {
  const activeRuns = runs.filter((run) => run.status === "running" || run.status === "queued" || run.status === "assigned").length
  const failedRuns = runs.filter((run) => run.status === "failed").length
  const onlineAgents = agents.filter((agent) => agent.status === "online" && !agent.disabled && !agent.quarantined).length

  return (
    <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
      <MetricCard title="API" value={apiStatus} description="Control plane status" icon={CheckCircle2} />
      <MetricCard title="Agents" value={`${onlineAgents}/${agents.length}`} description="Online registered agents" icon={Cloud} />
      <MetricCard title="Topologies" value={deployedTopologies.length} description={`${topologies.length} HCL artifacts total`} icon={Database} />
      <MetricCard title="Runs" value={activeRuns} description={`${failedRuns} failed in history`} icon={Play} />
    </div>
  )
}

function MetricCard({ title, value, description, icon: Icon }: { title: string; value: string | number; description: string; icon: typeof Cloud }) {
  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between border-b bg-muted/25">
        <div>
          <div className="sysbox-eyebrow">{title}</div>
          <CardTitle className="mt-2 text-2xl">{value}</CardTitle>
        </div>
        <Icon />
      </CardHeader>
      <CardContent>
        <p className="text-sm text-muted-foreground">{description}</p>
      </CardContent>
    </Card>
  )
}

function AgentsListPage({ agents }: { agents: Agent[] }) {
  const navigate = useNavigate()
  return (
    <EuiPanel title="Agents" description="Registered executors and their advertised capabilities.">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Agent</TableHead>
            <TableHead>Mode</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Capabilities</TableHead>
            <TableHead>Protocol</TableHead>
            <TableHead>Last heartbeat</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {agents.length === 0 ? (
            <TableRow>
              <TableCell colSpan={6}>
                <EmptyLine text="No agents registered" />
              </TableCell>
            </TableRow>
          ) : (
            agents.map((agent) => (
              <TableRow
                key={agent.id}
                className="cursor-pointer"
                tabIndex={0}
                onClick={() => navigate(`/agents/${encodeURIComponent(agent.id)}`)}
                onKeyDown={(event) => {
                  if (event.key === "Enter" || event.key === " ") {
                    event.preventDefault()
                    navigate(`/agents/${encodeURIComponent(agent.id)}`)
                  }
                }}
              >
                <TableCell>
                  <div className="font-medium">{agent.name || agent.id}</div>
                  <div className="font-mono text-xs text-muted-foreground">{agent.id}</div>
                </TableCell>
                <TableCell>{agent.labels?.execution === "in-process" ? "local API" : agent.labels?.mode || "agent"}</TableCell>
                <TableCell>
                  <StatusBadge status={agent.disabled ? "disabled" : agent.quarantined ? "quarantined" : agent.status} />
                </TableCell>
                <TableCell className="max-w-72 truncate">{agent.capabilities?.join(", ") || "none"}</TableCell>
                <TableCell>{agent.protocol || "unknown"}</TableCell>
                <TableCell className="text-muted-foreground">{agent.last_heartbeat || ""}</TableCell>
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>
      <p className="mt-3 text-xs text-muted-foreground">
        local API is the in-process fallback executor; local-docker is the Docker compose agent registered through the agent protocol.
      </p>
    </EuiPanel>
  )
}

function AgentDetailRoute({ agents, runs }: { agents: Agent[]; runs: Run[] }) {
  const { agentId = "" } = useParams()
  const agent = agents.find((item) => item.id === agentId)
  const agentRuns = runs.filter((run) => run.agent_id === agentId).slice(0, 12)

  if (!agent) {
    return <ResourceNotFound backTo="/agents" title="Agent not found" />
  }

  return (
    <div className="flex flex-col gap-4">
      <DetailHeader backTo="/agents" eyebrow="agents" title={agent.name || agent.id} description={agent.id} />
      <div className="grid gap-4 xl:grid-cols-[360px_1fr]">
        <Card>
          <CardHeader>
            <CardTitle>Agent state</CardTitle>
            <CardDescription>Identity, protocol, and current availability.</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3 text-sm">
            <KeyValue label="Status" value={<StatusBadge status={agent.disabled ? "disabled" : agent.quarantined ? "quarantined" : agent.status} />} />
            <KeyValue label="Mode" value={agent.labels?.execution === "in-process" ? "local API" : agent.labels?.mode || "agent"} />
            <KeyValue label="Protocol" value={agent.protocol || "unknown"} />
            <KeyValue label="Version" value={agent.version || "unknown"} />
            <KeyValue label="Last heartbeat" value={agent.last_heartbeat || "never"} />
          </CardContent>
        </Card>
        <div className="flex flex-col gap-4">
          <EuiPanel title="Capabilities" description="Substrates and operations advertised by this agent.">
            <div className="flex flex-wrap gap-2 p-4">
              {(agent.capabilities || []).length === 0 ? <EmptyLine text="No capabilities" /> : agent.capabilities?.map((capability) => <Badge key={capability} variant="secondary">{capability}</Badge>)}
            </div>
          </EuiPanel>
          <EuiPanel title="Recent runs" description="Runs assigned to this agent.">
            <RunsTable runs={agentRuns} />
          </EuiPanel>
        </div>
      </div>
    </div>
  )
}

function ArtifactsListPage({ topologies }: { topologies: Topology[] }) {
  const navigate = useNavigate()
  return (
    <EuiPanel title="HCL artifacts" description={`${topologies.length} saved configuration units`}>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Name</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Resources</TableHead>
            <TableHead>Serial</TableHead>
            <TableHead>Backend</TableHead>
            <TableHead>Revision</TableHead>
            <TableHead>Action</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {topologies.length === 0 ? (
            <TableRow>
              <TableCell colSpan={7}>
                <EmptyLine text="No HCL artifacts yet" />
              </TableCell>
            </TableRow>
          ) : (
            topologies.map((topology) => (
              <TableRow
                key={topology.name}
                className="cursor-pointer"
                tabIndex={0}
                onKeyDown={(event) => {
                  if (event.key === "Enter" || event.key === " ") {
                    event.preventDefault()
                    navigate(`/artifacts/${encodeURIComponent(topology.name)}`)
                  }
                }}
                onClick={() => navigate(`/artifacts/${encodeURIComponent(topology.name)}`)}
              >
                <TableCell className="font-medium">{topology.name}</TableCell>
                <TableCell>
                  <StatusBadge status={topology.has_state ? "applied" : "draft"} />
                </TableCell>
                <TableCell>{topology.resource_count || 0}</TableCell>
                <TableCell>{topology.serial || 0}</TableCell>
                <TableCell>{topology.backend || "local"}</TableCell>
                <TableCell className="font-mono text-xs text-muted-foreground">{topology.latest_revision || ""}</TableCell>
                <TableCell>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={(event) => {
                      event.stopPropagation()
                      navigate(`/artifacts/${encodeURIComponent(topology.name)}`)
                    }}
                  >
                    <MousePointer2 data-icon="inline-start" />
                    Open
                  </Button>
                </TableCell>
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>
    </EuiPanel>
  )
}

function ArtifactDetailRoute({
  topologies,
  runs,
  detail,
  busy,
  onCreatePlan,
  onApplyPlan,
  onDestroy,
  onDelete,
  onSaveHcl,
  onHclChange,
}: {
  topologies: Topology[]
  runs: Run[]
  detail: Detail
  busy: string
  onCreatePlan: () => void
  onApplyPlan: () => void
  onDestroy: () => void
  onDelete: () => void
  onSaveHcl: () => void
  onHclChange: (hcl: string) => void
}) {
  const { artifactId = "" } = useParams()
  const artifactName = decodeURIComponent(artifactId)
  const artifact = topologies.find((topology) => topology.name === artifactName)
  const artifactRuns = runs.filter((run) => run.topology === artifactName || run.workspace === artifactName).slice(0, 8)

  if (!artifact) {
    return <ResourceNotFound backTo="/artifacts" title="Artifact not found" />
  }

  return (
    <div className="flex flex-col gap-4">
      <DetailHeader backTo="/artifacts" eyebrow="artifacts" title={artifact.name} description="HCL configuration artifact" />
      <section className="min-w-0">
          <EuiPanel title={artifact.name} description={`${artifact.backend || "local"} · serial ${artifact.serial || 0}`}>
            <CardHeader>
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="flex items-center gap-2 text-sm text-muted-foreground">
                  <FileCode2 />
                  HCL artifact detail
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <Button variant="outline" onClick={onCreatePlan} disabled={busy !== ""}>
                    <GitBranch data-icon="inline-start" />
                    Plan
                  </Button>
                  <Button onClick={onApplyPlan} disabled={busy !== ""}>
                    <Play data-icon="inline-start" />
                    Apply
                  </Button>
                  <Button variant="outline" onClick={onDestroy} disabled={busy !== "" || !artifact.has_state}>
                    Destroy
                  </Button>
                  <Button variant="destructive" size="icon" onClick={onDelete} disabled={busy !== ""} aria-label="Delete">
                    <Trash2 />
                  </Button>
                </div>
              </div>
            </CardHeader>
            <CardContent>
              <Tabs defaultValue="hcl">
                <TabsList>
                  <TabsTrigger value="hcl">HCL</TabsTrigger>
                  <TabsTrigger value="plan">Plan</TabsTrigger>
                  <TabsTrigger value="runs">Runs</TabsTrigger>
                </TabsList>

                <TabsContent value="hcl">
                  <div className="flex flex-col gap-3">
                    <Textarea className="min-h-[520px] font-mono" value={detail.hcl || ""} onChange={(event) => onHclChange(event.target.value)} />
                    <div className="flex justify-end">
                      <Button onClick={onSaveHcl} disabled={busy !== ""}>
                        <FileCode2 data-icon="inline-start" />
                        Save HCL
                      </Button>
                    </div>
                  </div>
                </TabsContent>

                <TabsContent value="plan">
                  <PlanView plan={detail.plan || detail.plans?.[0]} />
                </TabsContent>

                <TabsContent value="runs">
                  <RunsTable runs={artifactRuns} />
                </TabsContent>
              </Tabs>
            </CardContent>
          </EuiPanel>
      </section>
    </div>
  )
}

function TopologiesListPage({
  topologies,
}: {
  topologies: Topology[]
}) {
  const navigate = useNavigate()
  return (
    <div className="flex flex-col gap-4">
      <EuiPanel title="Online topologies" description={`${topologies.length} deployed HCL artifacts`}>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Resources</TableHead>
              <TableHead>Serial</TableHead>
              <TableHead>Backend</TableHead>
              <TableHead>Action</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
          {topologies.length === 0 ? (
            <TableRow>
              <TableCell colSpan={6}>
                <EmptyLine text="No deployed topologies" />
              </TableCell>
            </TableRow>
          ) : (
            topologies.map((topology) => (
              <TableRow
                key={topology.name}
                className="cursor-pointer"
                tabIndex={0}
                onKeyDown={(event) => {
                  if (event.key === "Enter" || event.key === " ") {
                    event.preventDefault()
                    navigate(`/topologies/${encodeURIComponent(topology.name)}`)
                  }
                }}
                onClick={() => navigate(`/topologies/${encodeURIComponent(topology.name)}`)}
              >
                <TableCell className="font-medium">{topology.name}</TableCell>
                <TableCell>
                  <StatusBadge status="online" />
                </TableCell>
                <TableCell>{topology.resource_count || 0}</TableCell>
                <TableCell>{topology.serial || 0}</TableCell>
                <TableCell>{topology.backend || "local"}</TableCell>
                <TableCell>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={(event) => {
                      event.stopPropagation()
                      navigate(`/topologies/${encodeURIComponent(topology.name)}`)
                    }}
                  >
                    <MousePointer2 data-icon="inline-start" />
                    Open
                  </Button>
                </TableCell>
              </TableRow>
            ))
          )}
          </TableBody>
        </Table>
      </EuiPanel>
    </div>
  )
}

function TopologyDetailRoute({ topologies, detail, onConsole }: { topologies: Topology[]; detail: Detail; onConsole: (node: string) => void }) {
  const { topologyId = "" } = useParams()
  const topologyName = decodeURIComponent(topologyId)
  const topology = topologies.find((item) => item.name === topologyName)

  if (!topology) {
    return <ResourceNotFound backTo="/topologies" title="Topology not found" />
  }

  return (
    <div className="flex flex-col gap-4">
      <DetailHeader backTo="/topologies" eyebrow="topologies" title={topology.name} description="Deployed topology environment" />
      <div className="grid gap-4 xl:grid-cols-[1fr_320px]">
        <TopologyGraph nodes={detail.graph?.nodes || []} edges={detail.graph?.edges || []} />
        <div className="flex flex-col gap-4">
          <SummaryCard title="Health" value={detail.health?.status || "unknown"} icon={CheckCircle2} />
          <OutputsCard outputs={detail.outputs || {}} />
          <NodesCard topology={topology.name} nodes={detail.nodes || []} onConsole={onConsole} />
          <ResourcesTable resources={detail.resources || []} />
        </div>
      </div>
    </div>
  )
}

function EuiPanel({ title, description, children }: { title: string; description?: string; children: React.ReactNode }) {
  return (
    <Card className="sysbox-panel-glow rounded-md">
      <CardHeader className="border-b bg-muted/35 py-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <div className="sysbox-eyebrow">resource view</div>
            <CardTitle className="mt-1 text-sm">{title}</CardTitle>
            {description ? <CardDescription>{description}</CardDescription> : null}
          </div>
          <div className="flex items-center gap-2 rounded-md border bg-background/70 px-2 py-1 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
            <Filter />
            EUI table
          </div>
        </div>
      </CardHeader>
      <CardContent className="p-0">{children}</CardContent>
    </Card>
  )
}

function DetailHeader({ backTo, eyebrow, title, description }: { backTo: string; eyebrow: string; title: string; description: string }) {
  const navigate = useNavigate()
  return (
    <div className="flex flex-wrap items-center justify-between gap-3">
      <div>
        <div className="sysbox-eyebrow">{eyebrow}</div>
        <h2 className="mt-1 text-lg font-semibold tracking-normal">{title}</h2>
        <p className="text-sm text-muted-foreground">{description}</p>
      </div>
      <Button variant="outline" onClick={() => navigate(backTo)}>
        <ArrowLeft data-icon="inline-start" />
        Back
      </Button>
    </div>
  )
}

function ResourceNotFound({ backTo, title }: { backTo: string; title: string }) {
  return (
    <div className="flex flex-col gap-4">
      <DetailHeader backTo={backTo} eyebrow="not found" title={title} description="The requested resource was not returned by the API." />
      <EmptyLine text={title} />
    </div>
  )
}

function KeyValue({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-3 rounded-md border px-3 py-2">
      <span className="text-xs uppercase tracking-[0.12em] text-muted-foreground">{label}</span>
      <span className="min-w-0 truncate text-right">{value}</span>
    </div>
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
