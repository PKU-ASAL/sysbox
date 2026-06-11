import { useCallback, useEffect, useMemo, useState } from "react"
import { Navigate, Route, Routes, useLocation, useNavigate, useParams } from "react-router-dom"
import {
  Activity,
  CheckCircle2,
  Cloud,
  ClipboardList,
  FileCode2,
  FolderKanban,
  GitBranch,
  Moon,
  MousePointer2,
  Network,
  Play,
  Plus,
  RefreshCw,
  ServerCog,
  SquareTerminal,
  Sun,
  Trash2,
  X,
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import { usePolling } from "@/hooks/usePolling"
import { api } from "@/lib/api"
import { cn } from "@/lib/utils"
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

type ToastNotice = {
  id: string
  title: string
  description?: string
  variant?: "default" | "destructive"
}

export default function App() {
  const location = useLocation()
  const navigate = useNavigate()
  const [detail, setDetail] = useState<Detail>({})
  const [selectedCanvasNode, setSelectedCanvasNode] = useState<GraphNode | undefined>()
  const [toasts, setToasts] = useState<ToastNotice[]>([])
  const [busy, setBusy] = useState("")
  const [createOpen, setCreateOpen] = useState(false)
  const [newName, setNewName] = useState("docker-service")
  const [newHcl] = useState(starterHcl)
  const [consoleTarget, setConsoleTarget] = useState<{ topology: string; node: string; health?: ResourceHealth } | undefined>()
  const [selectedAgentID, setSelectedAgentID] = useState("auto")
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
  const selectedName = resolveRouteName(activePage, selectedResource, topologies)

  const refreshDetail = useCallback(async () => {
    if (!selectedName || (activePage !== "workspaces" && activePage !== "topologies")) {
      setDetail({})
      return
    }
    const result: Detail = {}
    const tasks = await Promise.allSettled([
      api.getHcl(selectedName),
      api.plans(selectedName),
      api.outputs(selectedName),
      api.healthOfTopology(selectedName),
      api.resources(selectedName),
      api.nodes(selectedName),
      api.graph(selectedName),
    ])
    if (tasks[0].status === "fulfilled") result.hcl = tasks[0].value
    if (tasks[1].status === "fulfilled") result.plans = tasks[1].value.plans
    if (tasks[2].status === "fulfilled") result.outputs = tasks[2].value.outputs
    if (tasks[3].status === "fulfilled") result.health = tasks[3].value
    if (tasks[4].status === "fulfilled") {
      result.resources = tasks[4].value.resources
      if (tasks[4].value.health) result.health = tasks[4].value.health
    }
    if (tasks[5].status === "fulfilled") result.nodes = tasks[5].value.nodes
    if (tasks[6].status === "fulfilled") result.graph = { nodes: tasks[6].value.nodes, edges: tasks[6].value.edges }
    setDetail(result)
  }, [activePage, selectedName])

  useEffect(() => {
    void refreshDetail()
  }, [refreshDetail])

  useEffect(() => {
    document.documentElement.classList.toggle("light", theme === "light")
    localStorage.setItem("sysbox.theme", theme)
  }, [theme])

  const dismissToast = useCallback((id: string) => {
    setToasts((current) => current.filter((toast) => toast.id !== id))
  }, [])

  const pushToast = useCallback(
    (toast: Omit<ToastNotice, "id">) => {
      const id = globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random()}`
      setToasts((current) => [...current.slice(-3), { ...toast, id }])
      window.setTimeout(() => dismissToast(id), 4200)
    },
    [dismissToast],
  )

  async function mutate(label: string, fn: () => Promise<unknown>) {
    setBusy(label)
    try {
      await fn()
      await overview.refresh()
      await refreshDetail()
      pushToast({ title: mutationSuccessTitle(label) })
    } catch (err) {
      pushToast({
        title: "Operation failed",
        description: err instanceof Error ? err.message : String(err),
        variant: "destructive",
      })
    } finally {
      setBusy("")
    }
  }

  async function createTopology() {
    await mutate("create topology", async () => {
      await api.createTopology(newName, newHcl)
      navigate(`/workspaces/${encodeURIComponent(workspaceID({ name: newName }))}`)
      setCreateOpen(false)
    })
  }

  async function saveHcl() {
    if (!selectedName || detail.hcl === undefined) return
    await mutate("save HCL", () => api.updateHcl(selectedName, detail.hcl || ""))
  }

  async function createPlan() {
    if (!selectedName) return
    await mutate("create plan", async () => {
      const plan = await api.createPlan(selectedName)
      setDetail((prev) => ({ ...prev, plan }))
    })
  }

  async function applyPlan() {
    if (!selectedName) return
    setBusy("apply")
    try {
      const planID = detail.plan?.id || detail.plans?.[0]?.id
      const run = await api.apply(selectedName, planID, selectedAgentID === "auto" ? undefined : selectedAgentID)
      pushToast({ title: "Apply started", description: run.run_id })
      await overview.refresh()
      void watchRun(run.run_id, "apply")
    } catch (err) {
      pushToast({
        title: "Operation failed",
        description: err instanceof Error ? err.message : String(err),
        variant: "destructive",
      })
    } finally {
      setBusy("")
    }
  }

  async function destroyTopology() {
    if (!selectedName) return
    setBusy("destroy")
    try {
      const run = await api.destroy(selectedName)
      pushToast({ title: "Destroy started", description: run.run_id })
      await overview.refresh()
      void watchRun(run.run_id, "destroy")
    } catch (err) {
      pushToast({
        title: "Operation failed",
        description: err instanceof Error ? err.message : String(err),
        variant: "destructive",
      })
    } finally {
      setBusy("")
    }
  }

  async function repairTopology(name: string) {
    if (!name) return
    setBusy("repair")
    try {
      const run = await api.repair(name, selectedAgentID === "auto" ? undefined : selectedAgentID)
      pushToast({ title: "Repair started", description: run.run_id })
      await overview.refresh()
      void watchRun(run.run_id, "repair")
    } catch (err) {
      pushToast({
        title: "Repair failed",
        description: err instanceof Error ? err.message : String(err),
        variant: "destructive",
      })
    } finally {
      setBusy("")
    }
  }

  async function deleteTopology() {
    if (!selectedName) return
    const name = selectedName
    await mutate("delete topology", async () => {
      await api.deleteTopology(name)
      navigate("/workspaces")
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

  async function watchRun(id: string, operation: "apply" | "destroy" | "repair") {
    try {
      await waitRun(id)
      await overview.refresh()
      await refreshDetail()
      pushToast({ title: runSuccessTitle(operation), description: id })
    } catch (err) {
      pushToast({
        title: runFailureTitle(operation),
        description: err instanceof Error ? err.message : String(err),
        variant: "destructive",
      })
    }
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
        <header className="sticky top-0 z-10 flex min-h-12 shrink-0 items-center gap-3 border-b bg-background/85 px-4 py-2 backdrop-blur lg:px-5">
          <SidebarTrigger className="-ml-1" />
          <div className="flex min-w-0 flex-1 flex-wrap items-center justify-between gap-3">
            <div>
              <h1 className="text-base font-semibold tracking-normal">{pageTitle}</h1>
              <p className="text-xs text-muted-foreground">{pageDescription}</p>
            </div>
            <div className="flex items-center gap-2">
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
              {activePage === "workspaces" ? (
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
                      <DialogDescription>Create an isolated experiment environment, then edit its HCL inside the workspace.</DialogDescription>
                    </DialogHeader>
                    <div className="flex flex-col gap-2">
                      <Label htmlFor="workspace-name">Name</Label>
                      <Input id="workspace-name" value={newName} onChange={(event) => setNewName(event.target.value)} />
                      <p className="text-sm text-muted-foreground">A starter HCL file will be created for this workspace.</p>
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
          <Routes>
            <Route path="/" element={<Navigate to="/workspaces" replace />} />
            <Route path="/workspaces" element={<WorkspacesListPage topologies={topologies} />} />
            <Route path="/workspaces/:workspaceId" element={<WorkspaceDetailRoute agents={agents} selectedAgentID={selectedAgentID} onAgentChange={setSelectedAgentID} topologies={topologies} runs={runs} detail={detail} busy={busy} onCreatePlan={createPlan} onApplyPlan={applyPlan} onDestroy={destroyTopology} onDelete={deleteTopology} onSaveHcl={saveHcl} onHclChange={(hcl) => setDetail((prev) => ({ ...prev, hcl }))} />} />
            <Route path="/runs" element={<RunsPage runs={runs} />} />
            <Route path="/topologies" element={<TopologiesListPage topologies={deployedTopologies} />} />
            <Route path="/topologies/:topologyId" element={<TopologyDetailRoute topologies={deployedTopologies} detail={detail} selectedNode={selectedCanvasNode} onSelectNode={setSelectedCanvasNode} onConsole={setConsoleTarget} onRepair={repairTopology} />} />
            <Route path="/agents" element={<AgentsListPage agents={agents} />} />
            <Route path="/agents/:agentId" element={<AgentDetailRoute agents={agents} runs={runs} />} />
            <Route path="/system" element={<SystemPage topologies={topologies} deployedTopologies={deployedTopologies} agents={agents} runs={runs} apiStatus={overview.data?.health.status || (overview.error ? "offline" : "checking")} />} />
            <Route path="/artifacts" element={<Navigate to="/workspaces" replace />} />
            <Route path="/artifacts/:artifactId" element={<Navigate to={`/workspaces/${selectedResource}`} replace />} />
            <Route path="*" element={<Navigate to="/workspaces" replace />} />
          </Routes>
        </div>
      </SidebarInset>

      <ConsoleDialog
        topology={consoleTarget?.topology || ""}
        node={consoleTarget?.node}
        nodeHealth={consoleTarget?.health}
        open={Boolean(consoleTarget)}
        onRepair={() => consoleTarget && void repairTopology(consoleTarget.topology)}
        onOpenChange={(open) => !open && setConsoleTarget(undefined)}
      />
      <ToastStack toasts={toasts} onDismiss={dismissToast} />
    </SidebarProvider>
  )
}

function mutationSuccessTitle(label: string) {
  return {
    "create topology": "Workspace created",
    "save HCL": "HCL saved",
    "create plan": "Plan created",
    apply: "Apply completed",
    destroy: "Destroy completed",
    "delete topology": "Workspace deleted",
  }[label] || `${label} completed`
}

function runSuccessTitle(operation: "apply" | "destroy" | "repair") {
  if (operation === "destroy") return "Destroy completed"
  if (operation === "repair") return "Repair completed"
  return "Apply completed"
}

function runFailureTitle(operation: "apply" | "destroy" | "repair") {
  if (operation === "destroy") return "Destroy failed"
  if (operation === "repair") return "Repair failed"
  return "Apply failed"
}

function ToastStack({ toasts, onDismiss }: { toasts: ToastNotice[]; onDismiss: (id: string) => void }) {
  return (
    <div className="fixed bottom-4 right-4 z-50 flex w-[min(24rem,calc(100vw-2rem))] flex-col gap-2">
      {toasts.map((toast) => (
        <div
          key={toast.id}
          className={cn(
            "rounded-md border bg-card px-4 py-3 text-sm shadow-lg",
            toast.variant === "destructive" ? "border-destructive/40" : "border-border",
          )}
        >
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <div className="font-medium">{toast.title}</div>
              {toast.description ? <div className="mt-1 text-xs text-muted-foreground">{toast.description}</div> : null}
            </div>
            <Button variant="ghost" size="icon" className="-mr-2 -mt-2" onClick={() => onDismiss(toast.id)} aria-label="Dismiss notification">
              <X />
            </Button>
          </div>
        </div>
      ))}
    </div>
  )
}

function pageFromPath(pathname: string): AppPage {
  if (pathname.startsWith("/runs")) return "runs"
  if (pathname.startsWith("/topologies")) return "topologies"
  if (pathname.startsWith("/agents")) return "agents"
  if (pathname.startsWith("/system")) return "system"
  return "workspaces"
}

function selectedResourceFromPath(pathname: string) {
  const segments = pathname.split("/").filter(Boolean)
  if ((segments[0] === "workspaces" || segments[0] === "artifacts" || segments[0] === "topologies") && segments[1]) {
    return decodeURIComponent(segments[1])
  }
  return ""
}

function resolveRouteName(page: AppPage, routeID: string, topologies: Topology[]) {
  if (!routeID) return ""
  if (page === "workspaces") {
    const match = topologies.find((topology) => workspaceID(topology) === routeID || artifactID(topology) === routeID)
    if (match) return match.name
    return routeID.startsWith("art_") ? "" : routeID
  }
  if (page === "topologies") {
    const match = topologies.find((topology) => topologyID(topology) === routeID)
    if (match) return match.name
    return routeID.startsWith("topo_") ? "" : routeID
  }
  return routeID
}

function artifactID(topology: Pick<Topology, "name" | "artifact_id">) {
  return topology.artifact_id || `art_${topology.name}`
}

function workspaceID(topology: Pick<Topology, "name" | "artifact_id">) {
  return artifactID(topology)
}

function topologyID(topology: Pick<Topology, "name" | "topology_id">) {
  return topology.topology_id || `topo_${topology.name}`
}

function titleFromPath(pathname: string) {
  const segments = pathname.split("/").filter(Boolean)
  if (segments.length >= 2) return `${segments[0]}/${decodeURIComponent(segments[1])}`
  if (segments[0] === "workspaces") return "Workspaces"
  if (segments[0] === "runs") return "Runs"
  if (segments[0] === "agents") return "Agents"
  if (segments[0] === "topologies") return "Topologies"
  if (segments[0] === "system") return "System"
  return "Workspaces"
}

function descriptionFromPage(page: AppPage) {
  return {
    workspaces: "Create HCL, review revisions, plan changes, and apply labs.",
    runs: "Track asynchronous apply, destroy, recover, and cleanup operations.",
    topologies: "Inspect deployed lab graphs, resources, outputs, and node consoles.",
    agents: "Registered executors connected to the control plane.",
    system: "Control-plane health, capacity, and operational inventory.",
  }[page]
}

function SystemPage({
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
    <div className="flex flex-col gap-4">
      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
        <MetricCard title="API" value={apiStatus} description="Control plane status" icon={CheckCircle2} />
        <MetricCard title="Agents" value={`${onlineAgents}/${agents.length}`} description="Online registered agents" icon={Cloud} />
        <MetricCard title="Workspaces" value={topologies.length} description={`${deployedTopologies.length} deployed topologies`} icon={FolderKanban} />
        <MetricCard title="Runs" value={activeRuns} description={`${failedRuns} failed in history`} icon={Play} />
      </div>
      <div className="grid gap-4 xl:grid-cols-2">
        <EuiPanel title="Registered agents" description="Execution nodes available to the scheduler.">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Agent</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Capabilities</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {agents.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={3}>
                    <EmptyLine text="No agents registered" />
                  </TableCell>
                </TableRow>
              ) : (
                agents.map((agent) => (
                  <TableRow key={agent.id}>
                    <TableCell className="font-medium">{agent.name || agent.id}</TableCell>
                    <TableCell>
                      <StatusBadge status={agent.status} />
                    </TableCell>
                    <TableCell className="max-w-72 truncate">{agent.capabilities?.join(", ") || "none"}</TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </EuiPanel>
        <EuiPanel title="Recent runs" description="Most recent control-plane activity.">
          <RunsTable runs={runs.slice(0, 8)} />
        </EuiPanel>
      </div>
    </div>
  )
}

function MetricCard({ title, value, description, icon: Icon }: { title: string; value: string | number; description: string; icon: typeof Cloud }) {
  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between border-b bg-muted/25 px-4 py-3">
        <div>
          <div className="sysbox-eyebrow">{title}</div>
          <CardTitle className="mt-2 text-2xl">{value}</CardTitle>
        </div>
        <Icon />
      </CardHeader>
      <CardContent className="flex min-h-10 items-center px-4 py-2">
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
      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
        <MetricCard title="State" value={agent.disabled ? "disabled" : agent.quarantined ? "quarantined" : agent.status} description={agent.name || agent.id} icon={Cloud} />
        <MetricCard title="Capabilities" value={(agent.capabilities || []).length} description={(agent.capabilities || []).slice(0, 3).join(", ") || "none advertised"} icon={Cloud} />
        <MetricCard title="Protocol" value={agent.protocol || "unknown"} description={agent.version || "version unknown"} icon={GitBranch} />
        <MetricCard title="Runs" value={agentRuns.length} description="Recent assigned operations" icon={Play} />
      </div>
      <EuiPanel title="Recent runs" description={`Agent ${agent.id}`}>
        <RunsTable runs={agentRuns} />
      </EuiPanel>
      <Card>
        <CardHeader>
          <CardTitle>Capabilities</CardTitle>
          <CardDescription>Substrates and operations advertised by this agent.</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-wrap gap-2">
          {(agent.capabilities || []).length === 0 ? <EmptyLine text="No capabilities" /> : agent.capabilities?.map((capability) => <Badge key={capability} variant="secondary">{capability}</Badge>)}
        </CardContent>
      </Card>
    </div>
  )
}

function RunsPage({ runs }: { runs: Run[] }) {
  const activeRuns = runs.filter((run) => run.status === "queued" || run.status === "assigned" || run.status === "running")
  const failedRuns = runs.filter((run) => run.status === "failed")
  const completedRuns = runs.filter((run) => run.status === "done")

  return (
    <div className="flex h-[calc(100vh-6.5rem)] min-h-0 flex-col gap-4 overflow-hidden">
      <div className="grid gap-4 md:grid-cols-3">
        <MetricCard title="Active" value={activeRuns.length} description="Queued, assigned, or running" icon={Activity} />
        <MetricCard title="Completed" value={completedRuns.length} description="Successful runs" icon={CheckCircle2} />
        <MetricCard title="Failed" value={failedRuns.length} description="Recover or cleanup candidates" icon={ClipboardList} />
      </div>
      <EuiPanel title="Runs" description="Apply, destroy, recover, and cleanup history." className="flex min-h-0 flex-1 flex-col" contentClassName="min-h-0 flex-1">
        <RunsTable runs={runs} constrained />
      </EuiPanel>
    </div>
  )
}

function WorkspacesListPage({ topologies }: { topologies: Topology[] }) {
  const navigate = useNavigate()
  const draftWorkspaces = topologies.filter((topology) => !topology.has_state).length
  const deployedWorkspaces = topologies.filter((topology) => topology.has_state).length

  return (
    <div className="flex flex-col gap-4">
      <div className="grid gap-4 md:grid-cols-3">
        <MetricCard title="Workspaces" value={topologies.length} description="Independent experiment environments" icon={FolderKanban} />
        <MetricCard title="Drafts" value={draftWorkspaces} description="Not applied yet" icon={FileCode2} />
        <MetricCard title="Deployed" value={deployedWorkspaces} description="Backed by live state" icon={Network} />
      </div>
      <EuiPanel title="Workspace environments" description="Create an environment first, then edit HCL and run plans inside it.">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Workspace</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Resources</TableHead>
              <TableHead>Serial</TableHead>
              <TableHead>Backend</TableHead>
              <TableHead>HCL revision</TableHead>
              <TableHead>Action</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {topologies.length === 0 ? (
              <TableRow>
                <TableCell colSpan={7}>
                  <EmptyLine text="No workspaces yet" />
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
                      navigate(`/workspaces/${encodeURIComponent(workspaceID(topology))}`)
                    }
                  }}
                  onClick={() => navigate(`/workspaces/${encodeURIComponent(workspaceID(topology))}`)}
                >
                  <TableCell>
                    <div className="font-medium">{topology.name}</div>
                    <div className="font-mono text-xs text-muted-foreground">{workspaceID(topology)}</div>
                  </TableCell>
                  <TableCell>
                    <StatusBadge status={topology.has_state ? "applied" : "draft"} />
                  </TableCell>
                  <TableCell>{topology.resource_count || 0}</TableCell>
                  <TableCell>{topology.serial || 0}</TableCell>
                  <TableCell>{topology.backend || "local"}</TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground">{topology.latest_revision || "uncommitted"}</TableCell>
                  <TableCell>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={(event) => {
                        event.stopPropagation()
                        navigate(`/workspaces/${encodeURIComponent(workspaceID(topology))}`)
                      }}
                    >
                      <MousePointer2 data-icon="inline-start" />
                      Enter
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

function WorkspaceDetailRoute({
  agents,
  selectedAgentID,
  onAgentChange,
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
  agents: Agent[]
  selectedAgentID: string
  onAgentChange: (agentID: string) => void
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
  const { workspaceId = "" } = useParams()
  const workspaceNameFromRoute = decodeURIComponent(workspaceId)
  const workspace = topologies.find((topology) => workspaceID(topology) === workspaceNameFromRoute || topology.name === workspaceNameFromRoute)
  const workspaceName = workspace?.name || workspaceNameFromRoute
  const workspaceRuns = runs.filter((run) => run.topology === workspaceName || run.workspace === workspaceName).slice(0, 8)
  const latestPlan = detail.plan || detail.plans?.[0]
  const resources = detail.resources || []
  const nodes = detail.nodes || []
  const outputs = detail.outputs || {}
  const mainFileName = `${workspace?.name || workspaceName}.sysbox.hcl`
  const hclLineCount = (detail.hcl || "").split("\n").length

  if (!workspace) {
    return <ResourceNotFound backTo="/workspaces" title="Workspace not found" />
  }

  return (
    <div className="sysbox-panel-glow flex h-[calc(100vh-6.5rem)] min-h-[680px] flex-col overflow-hidden rounded-md border bg-card">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b bg-muted/30 px-4 py-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <FolderKanban />
            <h2 className="truncate text-sm font-semibold">{workspace.name}</h2>
            <StatusBadge status={workspace.has_state ? detail.health?.status || "applied" : "draft"} />
          </div>
          <p className="mt-1 font-mono text-xs text-muted-foreground">
            {workspaceID(workspace)} · revision {workspace.latest_revision || "uncommitted"} · serial {workspace.serial || 0}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Select value={selectedAgentID} onValueChange={onAgentChange}>
            <SelectTrigger className="w-48">
              <SelectValue placeholder="Auto select agent" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="auto">Auto select agent</SelectItem>
              {agents.filter((agent) => agent.id !== "local").map((agent) => (
                <SelectItem key={agent.id} value={agent.id}>
                  {agent.name || agent.id}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button variant="outline" onClick={onSaveHcl} disabled={busy !== ""}>
            <FileCode2 data-icon="inline-start" />
            Save
          </Button>
          <Button variant="outline" onClick={onCreatePlan} disabled={busy !== ""}>
            <GitBranch data-icon="inline-start" />
            Plan
          </Button>
          <Button onClick={onApplyPlan} disabled={busy !== ""}>
            <Play data-icon="inline-start" />
            Apply
          </Button>
          <Button variant="outline" onClick={onDestroy} disabled={busy !== "" || !workspace.has_state}>
            Destroy
          </Button>
          <Button variant="destructive" onClick={onDelete} disabled={busy !== ""}>
            <Trash2 data-icon="inline-start" />
            Delete
          </Button>
        </div>
      </div>

      <div className="grid min-h-0 flex-1 lg:grid-cols-[220px_minmax(0,1fr)]">
        <aside className="border-b bg-muted/15 lg:border-b-0 lg:border-r">
          <div className="border-b px-3 py-2">
            <div className="sysbox-eyebrow">HCL files</div>
          </div>
          <div className="p-2">
            <button className="flex w-full items-center justify-between gap-3 rounded-md bg-primary/10 px-3 py-2 text-left text-sm">
              <span className="flex min-w-0 items-center gap-2">
                <FileCode2 />
                <span className="truncate font-medium">{mainFileName}</span>
              </span>
              <Badge variant="secondary">{hclLineCount}</Badge>
            </button>
          </div>
        </aside>

        <main className="min-h-0 min-w-0">
          <Textarea
            className="h-full min-h-[420px] resize-none rounded-none border-0 bg-background/95 p-4 font-mono text-sm leading-6 shadow-none focus-visible:ring-0"
            value={detail.hcl || ""}
            onChange={(event) => onHclChange(event.target.value)}
          />
        </main>
      </div>

      <Tabs defaultValue="plan" className="border-t bg-background">
        <div className="flex items-center justify-between gap-3 border-b px-3 py-2">
          <TabsList>
            <TabsTrigger value="plan">Plan</TabsTrigger>
            <TabsTrigger value="state">State</TabsTrigger>
            <TabsTrigger value="runs">Runs</TabsTrigger>
          </TabsList>
          <div className="min-w-0">
            <span className="font-mono text-xs text-muted-foreground">{workspace.backend || "local"} backend</span>
          </div>
        </div>
        <TabsContent value="plan" className="mt-0 max-h-72 overflow-auto p-4">
          <PlanView plan={latestPlan} />
        </TabsContent>
        <TabsContent value="state" className="mt-0 max-h-72 overflow-auto p-4">
          <WorkspaceStateTable workspace={workspace} health={detail.health} resources={resources} nodes={nodes} outputs={outputs} />
        </TabsContent>
        <TabsContent value="runs" className="mt-0 max-h-72 overflow-auto p-4">
          <RunsTable runs={workspaceRuns} />
        </TabsContent>
      </Tabs>
    </div>
  )
}

function WorkspaceStateTable({
  workspace,
  health,
  resources,
  nodes,
  outputs,
}: {
  workspace: Topology
  health?: TopologyHealth
  resources: ResourceHealth[]
  nodes: NodeInfo[]
  outputs: Record<string, OutputValue>
}) {
  return (
    <div className="rounded-md border">
      <Table>
        <TableBody>
          <TableRow>
            <TableCell>Health</TableCell>
            <TableCell>
              <StatusBadge status={workspace.has_state ? health?.status || "known" : "draft"} />
            </TableCell>
          </TableRow>
          <TableRow>
            <TableCell>Resources</TableCell>
            <TableCell>{workspace.resource_count || resources.length || 0}</TableCell>
          </TableRow>
          <TableRow>
            <TableCell>Nodes</TableCell>
            <TableCell>{nodes.length}</TableCell>
          </TableRow>
          <TableRow>
            <TableCell>Outputs</TableCell>
            <TableCell>{Object.keys(outputs).length}</TableCell>
          </TableRow>
          <TableRow>
            <TableCell>Backend</TableCell>
            <TableCell>{workspace.backend || "local"}</TableCell>
          </TableRow>
        </TableBody>
      </Table>
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
      <EuiPanel title="Online topologies" description={`${topologies.length} deployed workspaces`}>
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
                    navigate(`/topologies/${encodeURIComponent(topologyID(topology))}`)
                  }
                }}
                onClick={() => navigate(`/topologies/${encodeURIComponent(topologyID(topology))}`)}
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
                      navigate(`/topologies/${encodeURIComponent(topologyID(topology))}`)
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

function TopologyDetailRoute({
  topologies,
  detail,
  selectedNode,
  onSelectNode,
  onConsole,
  onRepair,
}: {
  topologies: Topology[]
  detail: Detail
  selectedNode?: GraphNode
  onSelectNode: (node: GraphNode | undefined) => void
  onConsole: (target: { topology: string; node: string; health?: ResourceHealth }) => void
  onRepair: (topology: string) => void
}) {
  const { topologyId = "" } = useParams()
  const topologyName = decodeURIComponent(topologyId)
  const topology = topologies.find((item) => topologyID(item) === topologyName || item.name === topologyName)
  const graphEdges = detail.graph?.edges || []
  const resourceHealth = detail.resources || []
  const graphNodes = enrichGraphNodes(detail.graph?.nodes || [], resourceHealth)
  const networkLinks = graphEdges.filter(isNetworkGraphEdge).length
  const dependencyLinks = graphEdges.length - networkLinks

  if (!topology) {
    return <ResourceNotFound backTo="/topologies" title="Topology not found" />
  }

  return (
    <div className="-m-4 lg:-m-6">
      <div className="grid min-h-[calc(100vh-3.5rem)] xl:grid-cols-[minmax(0,1fr)_380px]">
        <main className="min-w-0">
          <div className="flex flex-wrap items-center justify-between gap-3 border-b bg-muted/20 px-4 py-3">
            <div className="min-w-0">
              <div className="flex items-center gap-2">
                <Network />
                <h2 className="truncate text-sm font-semibold">{topology.name}</h2>
                <StatusBadge status={detail.health?.status || "online"} />
              </div>
              <p className="mt-1 font-mono text-xs text-muted-foreground">
                {topology.resource_count || detail.resources?.length || 0} resources · serial {topology.serial || 0}
              </p>
            </div>
            <div className="flex flex-wrap gap-2">
              <Badge variant="secondary">{topology.backend || "local"}</Badge>
              <Badge variant="secondary">{detail.nodes?.length || 0} nodes</Badge>
              <Badge variant="secondary">{networkLinks} net links</Badge>
              {dependencyLinks > 0 ? <Badge variant="secondary">{dependencyLinks} deps</Badge> : null}
            </div>
          </div>
          <TopologyGraph nodes={graphNodes} edges={graphEdges} onSelectNode={onSelectNode} />
        </main>
        <aside className="border-l bg-background/95 p-4">
          <Tabs defaultValue="inspect" className="flex flex-col gap-4">
            <TabsList className="grid w-full grid-cols-4">
              <TabsTrigger value="inspect">Inspect</TabsTrigger>
              <TabsTrigger value="nodes">Nodes</TabsTrigger>
              <TabsTrigger value="resources">Resources</TabsTrigger>
              <TabsTrigger value="outputs">Outputs</TabsTrigger>
            </TabsList>
            <TabsContent value="inspect" className="mt-0 flex flex-col gap-4">
              <CanvasSelectionCard node={selectedNode} onClear={() => onSelectNode(undefined)} />
              <SummaryCard title="Health" value={detail.health?.status || "unknown"} icon={CheckCircle2} />
              {hasDrift(resourceHealth) ? (
                <Button onClick={() => onRepair(topology.name)}>
                  <RefreshCw data-icon="inline-start" />
                  Repair drift
                </Button>
              ) : null}
            </TabsContent>
            <TabsContent value="nodes" className="mt-0">
              <NodesCard topology={topology.name} nodes={detail.nodes || []} resources={resourceHealth} onConsole={onConsole} />
            </TabsContent>
            <TabsContent value="resources" className="mt-0">
              <ResourcesTable resources={detail.resources || []} />
            </TabsContent>
            <TabsContent value="outputs" className="mt-0">
              <OutputsCard outputs={detail.outputs || {}} />
            </TabsContent>
          </Tabs>
        </aside>
      </div>
    </div>
  )
}

function isNetworkGraphEdge(edge: GraphEdge) {
  return edge.kind === "link" || edge.kind === "veth" || edge.kind === "route"
}

function enrichGraphNodes(nodes: GraphNode[], resources: ResourceHealth[]) {
  const healthByResource = new Map(resources.map((resource) => [resource.resource, resource]))
  return nodes.map((node) => {
    const health = healthByResource.get(node.id)
    if (!health) return node
    return {
      ...node,
      status: health.status || node.status,
      extra: {
        ...(node.extra || {}),
        health_reason: health.reason,
        decision: health.decision,
        observation_status: health.observation?.status,
        exit_code: health.observation?.exit_code,
      },
    }
  })
}

function hasDrift(resources: ResourceHealth[]) {
  return resources.some((resource) => resource.status === "drifted" || resource.decision === "mark_drift")
}

function EuiPanel({
  title,
  description,
  children,
  className,
  contentClassName,
}: {
  title: string
  description?: string
  children: React.ReactNode
  className?: string
  contentClassName?: string
}) {
  return (
    <Card className={cn("sysbox-panel-glow rounded-md", className)}>
      <CardHeader className="border-b bg-muted/35 py-3">
        <div>
          <CardTitle className="text-sm">{title}</CardTitle>
          {description ? <CardDescription>{description}</CardDescription> : null}
        </div>
      </CardHeader>
      <CardContent className={cn("p-0", contentClassName)}>{children}</CardContent>
    </Card>
  )
}

function ResourceNotFound({ backTo, title }: { backTo: string; title: string }) {
  const navigate = useNavigate()
  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader>
          <CardTitle>{title}</CardTitle>
          <CardDescription>The requested resource was not returned by the API.</CardDescription>
        </CardHeader>
        <CardContent>
          <Button variant="outline" onClick={() => navigate(backTo)}>
            Back
          </Button>
        </CardContent>
      </Card>
      <EmptyLine text={title} />
    </div>
  )
}

function CanvasSelectionCard({ node, onClear }: { node?: GraphNode; onClear: () => void }) {
  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <div>
          <CardTitle>Selection</CardTitle>
          <CardDescription>Click a canvas node to inspect it.</CardDescription>
        </div>
        {node ? (
          <Button variant="outline" size="sm" onClick={onClear}>
            Clear
          </Button>
        ) : null}
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {!node ? (
          <EmptyLine text="No node selected" />
        ) : (
          <>
            <div className="text-sm font-medium">{node.label}</div>
            <div className="font-mono text-xs text-muted-foreground">{node.id}</div>
            <div className="flex flex-wrap gap-2 pt-2">
              <Badge variant="secondary">{node.type}</Badge>
              <Badge variant="secondary">{node.status}</Badge>
              {node.substrate ? <Badge variant="secondary">{node.substrate}</Badge> : null}
            </div>
            {node.ip ? <div className="rounded-md border px-3 py-2 font-mono text-xs">{node.ip}</div> : null}
          </>
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

function NodesCard({
  topology,
  nodes,
  resources,
  onConsole,
}: {
  topology: string
  nodes: NodeInfo[]
  resources: ResourceHealth[]
  onConsole: (target: { topology: string; node: string; health?: ResourceHealth }) => void
}) {
  const healthByNode = new Map(resources.filter((resource) => resource.type === "sysbox_node" && resource.name).map((resource) => [resource.name, resource]))
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
          nodes.map((node) => {
            const health = healthByNode.get(node.name)
            return (
            <div key={node.name} className={cn("flex items-center justify-between gap-3 rounded-md border px-3 py-2", health?.status === "drifted" && "border-destructive/50")}>
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <span className="truncate text-sm font-medium">{node.name}</span>
                  {health?.status ? <StatusBadge status={health.status} /> : null}
                </div>
                <div className="truncate text-xs text-muted-foreground">
                  {node.provider}
                  {node.primary_ip ? ` · ${node.primary_ip}` : ""}
                  {health?.reason ? ` · ${health.reason}` : ""}
                </div>
              </div>
              <Button variant="outline" size="icon" aria-label="Open console" onClick={() => onConsole({ topology, node: node.name, health })}>
                <SquareTerminal />
              </Button>
            </div>
            )
          })
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

function RunsTable({ runs, constrained = false }: { runs: Run[]; constrained?: boolean }) {
  return (
    <div className={constrained ? "h-full min-h-0 overflow-auto rounded-md border" : "rounded-md border"}>
      <Table>
        <TableHeader className={constrained ? "sticky top-0 bg-card" : ""}>
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
