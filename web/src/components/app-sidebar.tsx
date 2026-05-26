import * as React from "react"
import {
  Activity,
  Boxes,
  Database,
  CloudCog,
  DatabaseZap,
  LayoutDashboard,
  Package,
  RadioTower,
} from "lucide-react"

import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
} from "@/components/ui/sidebar"
import type { Agent, Run, Topology } from "@/types/api"

export type AppPage = "dashboard" | "agents" | "artifacts" | "topologies"

type AppSidebarProps = React.ComponentProps<typeof Sidebar> & {
  activePage: AppPage
  apiStatus: string
  agents: Agent[]
  runs: Run[]
  topologies: Topology[]
  onPageChange: (page: AppPage) => void
}

const primaryNav = [
  { page: "dashboard", title: "Dashboard", icon: LayoutDashboard },
  { page: "agents", title: "Agents", icon: RadioTower, badge: "agents" },
  { page: "artifacts", title: "Artifacts", icon: Package, badge: "runs" },
  { page: "topologies", title: "Topologies", icon: Database, badge: "topologies" },
] satisfies Array<{ page: AppPage; title: string; icon: typeof LayoutDashboard; badge?: "agents" | "runs" | "topologies" }>

const pageHints = [
  { title: "HCL", description: "Create, plan, and apply" },
  { title: "Runs", description: "Task history and status" },
]

export function AppSidebar({ activePage, apiStatus, agents, runs, topologies, onPageChange, ...props }: AppSidebarProps) {
  const activeRuns = runs.filter((run) => run.status === "queued" || run.status === "assigned" || run.status === "running").length
  const onlineAgents = agents.filter((agent) => agent.status === "online" && !agent.disabled && !agent.quarantined).length
  const deployedTopologies = topologies.filter((topology) => topology.has_state).length

  function badgeValue(key?: string) {
    if (key === "topologies") return deployedTopologies
    if (key === "agents") return onlineAgents
    if (key === "runs") return activeRuns
    return undefined
  }

  return (
    <Sidebar collapsible="icon" {...props}>
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size="lg" tooltip="sysbox">
              <div className="flex aspect-square size-8 items-center justify-center rounded-lg bg-sidebar-primary text-sidebar-primary-foreground">
                <Boxes />
              </div>
              <div className="grid flex-1 text-left text-sm leading-tight">
                <span className="truncate font-semibold">sysbox</span>
                <span className="truncate text-xs">Control plane</span>
              </div>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupLabel>Platform</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {primaryNav.map((item) => {
                const value = badgeValue(item.badge)
                return (
                  <SidebarMenuItem key={item.title}>
                    <SidebarMenuButton tooltip={item.title} isActive={activePage === item.page} onClick={() => onPageChange(item.page)}>
                      <item.icon />
                      <span>{item.title}</span>
                    </SidebarMenuButton>
                    {value !== undefined ? <SidebarMenuBadge>{value}</SidebarMenuBadge> : null}
                  </SidebarMenuItem>
                )
              })}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>

        <SidebarGroup>
          <SidebarGroupLabel>Artifacts</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {pageHints.map((item) => (
                <SidebarMenuItem>
                  <SidebarMenuButton tooltip={item.description} onClick={() => onPageChange("artifacts")}>
                    <Package />
                    <span>{item.title}</span>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>

        <SidebarGroup>
          <SidebarGroupLabel>Agents</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {agents.length === 0 ? (
                <SidebarMenuItem>
                  <SidebarMenuButton tooltip="No agents" disabled>
                    <CloudCog />
                    <span>No agents</span>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ) : (
                agents.slice(0, 5).map((agent) => (
                  <SidebarMenuItem key={agent.id}>
                    <SidebarMenuButton tooltip={agent.name || agent.id}>
                      <CloudCog />
                      <span>{agent.name || agent.id}</span>
                    </SidebarMenuButton>
                    <SidebarMenuBadge>{agent.status}</SidebarMenuBadge>
                  </SidebarMenuItem>
                ))
              )}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>

      <SidebarFooter>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size="lg" tooltip={`API ${apiStatus}`}>
              <div className="flex aspect-square size-8 items-center justify-center rounded-lg bg-sidebar-accent text-sidebar-accent-foreground">
                <Activity />
              </div>
              <div className="grid flex-1 text-left text-sm leading-tight">
                <span className="truncate font-medium">API {apiStatus}</span>
                <span className="truncate text-xs">{activeRuns} active runs</span>
              </div>
            </SidebarMenuButton>
          </SidebarMenuItem>
          <SidebarMenuItem>
            <SidebarMenuButton tooltip="State backend">
              <DatabaseZap />
              <span>Postgres state</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  )
}
