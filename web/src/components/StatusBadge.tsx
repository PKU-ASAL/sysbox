import { Badge } from "@/components/ui/badge"

export function StatusBadge({ status }: { status?: string }) {
  const value = status || "unknown"
  const variant = value === "failed" || value === "unhealthy" || value === "drifted" ? "destructive" : value === "online" || value === "healthy" || value === "done" ? "default" : "secondary"

  return <Badge variant={variant}>{value}</Badge>
}

