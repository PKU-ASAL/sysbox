import * as React from "react"
import { useNavigate } from "react-router-dom"
import {
  Activity,
  Boxes,
  Database,
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
}

const primaryNav = [
  { page: "dashboard", title: "Dashboard", icon: LayoutDashboard },
  { page: "agents", title: "Agents", icon: RadioTower, badge: "agents" },
  { page: "artifacts", title: "Artifacts", icon: Package, badge: "runs" },
  { page: "topologies", title: "Topologies", icon: Database, badge: "topologies" },
] satisfies Array<{ page: AppPage; title: string; icon: typeof LayoutDashboard; badge?: "agents" | "runs" | "topologies" }>

export function AppSidebar({ activePage, apiStatus, agents, runs, topologies, ...props }: AppSidebarProps) {
  const navigate = useNavigate()
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
                <span className="truncate font-mono text-[10px] uppercase tracking-[0.16em] text-sidebar-foreground/70">Control plane</span>
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
                    <SidebarMenuButton tooltip={item.title} isActive={activePage === item.page} onClick={() => navigate(item.page === "dashboard" ? "/" : `/${item.page}`)}>
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
                <span className="truncate font-mono text-[10px] uppercase tracking-[0.16em] text-sidebar-foreground/70">{activeRuns} active runs</span>
              </div>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  )
}
