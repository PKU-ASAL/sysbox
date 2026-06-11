import type {
  Agent,
  ConsoleSession,
  GraphEdge,
  GraphNode,
  NodeInfo,
  OutputValue,
  Plan,
  ResourceHealth,
  Run,
  Topology,
  TopologyHealth,
} from "@/types/api"

type RequestOptions = {
  method?: string
  body?: unknown
  rawBody?: string
  headers?: Record<string, string>
  responseType?: "json" | "text"
}

export type ConsoleSessionRequest = {
  cmd?: string[]
  shell?: string
  tty?: boolean
  timeout_seconds?: number
  requested_by?: string
  roles?: string[]
}

export class ApiError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

export function getToken() {
  return localStorage.getItem("sysbox.api_token") || ""
}

export function setToken(token: string) {
  if (token.trim() === "") {
    localStorage.removeItem("sysbox.api_token")
  } else {
    localStorage.setItem("sysbox.api_token", token.trim())
  }
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { ...(options.headers || {}) }
  const token = getToken()
  if (token) {
    headers.Authorization = `Bearer ${token}`
  }
  let body: BodyInit | undefined
  if (options.rawBody !== undefined) {
    body = options.rawBody
  } else if (options.body !== undefined) {
    headers["Content-Type"] = "application/json"
    body = JSON.stringify(options.body)
  }
  const res = await fetch(path, {
    method: options.method || "GET",
    headers,
    body,
  })
  const text = await res.text()
  if (!res.ok) {
    let message = text || res.statusText
    try {
      const parsed = JSON.parse(text) as { error?: string }
      message = parsed.error || message
    } catch {
      // Keep plain text error.
    }
    throw new ApiError(res.status, message)
  }
  if (!text) {
    return undefined as T
  }
  if (options.responseType === "text") {
    return text as T
  }
  return JSON.parse(text) as T
}

export const api = {
  health: () => request<{ status: string }>("/v1/health"),
  agents: () => request<{ agents: Agent[] }>("/v1/agents"),
  topologies: () => request<{ topologies: Topology[] }>("/v1/topologies"),
  topology: (name: string) => request<Topology>(`/v1/topologies/${encodeURIComponent(name)}`),
  createTopology: (name: string, hcl: string) =>
    request<Topology>("/v1/topologies", { method: "POST", body: { name, hcl } }),
  getHcl: (name: string) => request<string>(`/v1/topologies/${encodeURIComponent(name)}/hcl`, { headers: { Accept: "text/plain" }, responseType: "text" }),
  updateHcl: (name: string, hcl: string) =>
    request<{ name: string; message: string }>(`/v1/topologies/${encodeURIComponent(name)}/hcl`, {
      method: "PUT",
      rawBody: hcl,
      headers: { "Content-Type": "text/plain" },
    }),
  createRevision: (name: string) => request<{ id: string }>(`/v1/topologies/${encodeURIComponent(name)}/revisions`, { method: "POST" }),
  createPlan: (name: string) => request<Plan>(`/v1/topologies/${encodeURIComponent(name)}/plans`, { method: "POST" }),
  plans: (name: string) => request<{ plans: Plan[] }>(`/v1/topologies/${encodeURIComponent(name)}/plans`),
  apply: (name: string, planID?: string, agentID?: string) =>
    request<{ run_id: string; agent_id?: string }>(`/v1/topologies/${encodeURIComponent(name)}/apply`, {
      method: "POST",
      body: { ...(planID ? { plan_id: planID } : {}), ...(agentID ? { agent_id: agentID } : {}) },
    }),
  destroy: (name: string) =>
    request<{ run_id: string; agent_id?: string }>(`/v1/topologies/${encodeURIComponent(name)}/destroy`, {
      method: "POST",
    }),
  deleteTopology: (name: string) => request<{ name: string; message: string }>(`/v1/topologies/${encodeURIComponent(name)}`, { method: "DELETE" }),
  run: (id: string) => request<Run>(`/v1/runs/${encodeURIComponent(id)}`),
  runs: () => request<{ runs: Run[] }>("/v1/runs"),
  outputs: (name: string) => request<{ outputs: Record<string, OutputValue> }>(`/v1/topologies/${encodeURIComponent(name)}/outputs`),
  healthOfTopology: (name: string) => request<TopologyHealth>(`/v1/topologies/${encodeURIComponent(name)}/health?cached=true`),
  resources: (name: string) =>
    request<{ resources: ResourceHealth[]; health?: TopologyHealth }>(`/v1/topologies/${encodeURIComponent(name)}/resources`),
  repair: (name: string, agentID?: string) =>
    request<{ run_id: string; agent_id?: string; operation?: string }>(`/v1/topologies/${encodeURIComponent(name)}/repair`, {
      method: "POST",
      body: { ...(agentID ? { agent_id: agentID } : {}) },
    }),
  graph: (name: string) =>
    request<{ topology: string; nodes: GraphNode[]; edges: GraphEdge[] }>(`/v1/topologies/${encodeURIComponent(name)}/graph`),
  nodes: (name: string) => request<{ nodes: NodeInfo[] }>(`/v1/topologies/${encodeURIComponent(name)}/nodes`),
  session: (id: string) => request<ConsoleSession>(`/v1/sessions/${encodeURIComponent(id)}`),
  createSession: (topology: string, node: string, sessionRequest: ConsoleSessionRequest) =>
    request<ConsoleSession>(`/v1/topologies/${encodeURIComponent(topology)}/nodes/${encodeURIComponent(node)}/sessions`, {
      method: "POST",
      body: { tty: true, timeout_seconds: 3600, requested_by: "web", roles: ["admin"], ...sessionRequest },
    }),
}

export function sessionAttachURL(sessionID: string) {
  const base = window.location.origin.replace(/^http:/, "ws:").replace(/^https:/, "wss:")
  const token = getToken()
  const path = `${base}/v1/sessions/${encodeURIComponent(sessionID)}/attach`
  return token ? `${path}?token=${encodeURIComponent(token)}` : path
}
