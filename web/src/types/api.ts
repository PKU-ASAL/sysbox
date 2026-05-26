export type Agent = {
  id: string
  name?: string
  status: string
  disabled?: boolean
  quarantined?: boolean
  reason?: string
  protocol?: string
  capabilities?: string[]
  labels?: Record<string, string>
  version?: string
  last_heartbeat?: string
}

export type Topology = {
  project_id?: string
  artifact_id?: string
  topology_id?: string
  name: string
  has_hcl: boolean
  has_state: boolean
  resource_count?: number
  serial?: number
  backend?: string
  updated_at?: string
  latest_revision?: string
}

export type PlanAction = {
  resource: string
  action: string
  reason?: string
  changes?: Record<string, unknown>
}

export type Plan = {
  id: string
  workspace: string
  revision?: string
  state_serial?: number
  status: string
  summary?: string
  actions: PlanAction[]
  created_at: string
}

export type Run = {
  id: string
  topology: string
  workspace?: string
  operation?: string
  op?: string
  status: "queued" | "assigned" | "running" | "done" | "failed" | "cancelled"
  error?: string
  plan_id?: string
  agent_id?: string
  started_at?: string
  ended_at?: string
  queued_at?: string
  assigned_at?: string
}

export type ResourceHealth = {
  resource?: string
  type?: string
  name?: string
  provider?: string
  status?: string
  reason?: string
  decision?: string
  checks?: Record<string, { ok?: boolean; message?: string }>
}

export type TopologyHealth = {
  status: string
  healthy?: number
  unhealthy?: number
  drifted?: number
  unknown?: number
  resources?: ResourceHealth[]
}

export type GraphNode = {
  id: string
  type: string
  label: string
  status: string
  substrate?: string
  ip?: string
  cidr?: string
  nat?: boolean
  extra?: Record<string, unknown>
}

export type GraphEdge = {
  from: string
  to: string
  kind: string
  label?: string
  ip?: string
}

export type NodeInfo = {
  name: string
  provider: string
  primary_ip?: string
}

export type ConsoleSession = {
  id: string
  topology: string
  node: string
  agent_id: string
  status: string
  error?: string
  exit_code?: number
  tty: boolean
  created_at: string
}

export type OutputValue = {
  value: unknown
  sensitive?: boolean
}
