import { useEffect, useRef, useState } from "react"
import { Terminal } from "@xterm/xterm"
import { FitAddon } from "@xterm/addon-fit"
import "@xterm/xterm/css/xterm.css"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { api, sessionAttachURL } from "@/lib/api"

type Props = {
  topology: string
  node?: string
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function ConsoleDialog({ topology, node, open, onOpenChange }: Props) {
  const terminalRef = useRef<HTMLDivElement | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const termRef = useRef<Terminal | null>(null)
  const [command, setCommand] = useState("/bin/sh")
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!open || !node || !terminalRef.current) {
      return
    }
    const term = new Terminal({
      cursorBlink: true,
      convertEol: true,
      fontFamily: "JetBrains Mono, ui-monospace, monospace",
      fontSize: 13,
      theme: {
        background: "#0f172a",
        foreground: "#dbeafe",
      },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(terminalRef.current)
    fit.fit()
    term.writeln(`sysbox console: ${topology}/${node}`)
    termRef.current = term

    return () => {
      wsRef.current?.close()
      wsRef.current = null
      term.dispose()
      termRef.current = null
    }
  }, [node, open, topology])

  async function start() {
    if (!node || !termRef.current) {
      return
    }
    setBusy(true)
    const term = termRef.current
    term.clear()
    term.writeln(`starting ${command}`)
    try {
      const cmd = command.trim() === "" ? ["/bin/sh"] : ["/bin/sh", "-lc", command]
      const session = await api.createSession(topology, node, cmd)
      const ws = new WebSocket(sessionAttachURL(session.id))
      wsRef.current = ws
      ws.onopen = () => term.writeln(`attached session ${session.id}`)
      ws.onmessage = (event) => {
        const frame = JSON.parse(event.data) as { type: string; data?: string; code?: number; error?: string }
        if (frame.type === "stdout" || frame.type === "stderr") {
          term.write(atob(frame.data || ""))
        } else if (frame.type === "exit") {
          term.writeln("")
          term.writeln(`exit ${frame.code ?? 0}`)
          setBusy(false)
        } else if (frame.type === "error") {
          term.writeln(frame.error || "session error")
          setBusy(false)
        }
      }
      ws.onclose = () => setBusy(false)
      term.onData((data) => {
        if (ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: "stdin", data: btoa(data) }))
        }
      })
    } catch (err) {
      term.writeln(err instanceof Error ? err.message : String(err))
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-4xl">
        <DialogHeader>
          <DialogTitle>Console</DialogTitle>
          <DialogDescription>{node ? `${topology}/${node}` : "Select a node to open a console session."}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div className="flex gap-2">
            <Input value={command} onChange={(event) => setCommand(event.target.value)} placeholder="/bin/sh" />
            <Button onClick={start} disabled={!node || busy}>
              Start
            </Button>
          </div>
          <div ref={terminalRef} className="h-96 overflow-hidden rounded-md border bg-slate-950" />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Close
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
