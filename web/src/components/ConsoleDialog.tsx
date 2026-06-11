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
import type { ResourceHealth } from "@/types/api"

type Props = {
  topology: string
  node?: string
  nodeHealth?: ResourceHealth
  open: boolean
  onRepair?: () => void
  onOpenChange: (open: boolean) => void
}

export function ConsoleDialog({ topology, node, nodeHealth, open, onRepair, onOpenChange }: Props) {
  const wsRef = useRef<WebSocket | null>(null)
  const termRef = useRef<Terminal | null>(null)
  const dataDisposableRef = useRef<{ dispose: () => void } | null>(null)
  const printedMessagesRef = useRef<Set<string>>(new Set())
  const [terminalHost, setTerminalHost] = useState<HTMLDivElement | null>(null)
  const [command, setCommand] = useState("/bin/sh")
  const [busy, setBusy] = useState(false)
  const [terminalReady, setTerminalReady] = useState(false)
  const [status, setStatus] = useState("")
  const nodeUnavailable =
    nodeHealth?.status === "drifted" ||
    nodeHealth?.status === "unknown" ||
    nodeHealth?.observation?.running === false

  useEffect(() => {
    setTerminalReady(false)
    setStatus("")
    if (!open || !node || !terminalHost) {
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
    try {
      term.loadAddon(fit)
      term.open(terminalHost)
      window.requestAnimationFrame(() => {
        try {
          fit.fit()
        } catch {
          // The terminal remains usable even if the first fit lands before layout settles.
        }
        term.writeln(`sysbox console: ${topology}/${node}`)
        termRef.current = term
        setTerminalReady(true)
      })
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err))
      term.dispose()
      return
    }

    return () => {
      wsRef.current?.close()
      wsRef.current = null
      dataDisposableRef.current?.dispose()
      dataDisposableRef.current = null
      term.dispose()
      termRef.current = null
      setTerminalReady(false)
    }
  }, [node, open, terminalHost, topology])

  async function start() {
    if (!node || !topology) {
      setStatus("Select a topology node before starting a console.")
      return
    }
    if (!termRef.current) {
      setStatus("Console is still preparing. Try again in a moment.")
      return
    }
    wsRef.current?.close()
    wsRef.current = null
    dataDisposableRef.current?.dispose()
    dataDisposableRef.current = null
    setBusy(true)
    setStatus("")
    const term = termRef.current
    term.clear()
    printedMessagesRef.current.clear()
    term.writeln(`starting ${command}`)
    try {
      const trimmed = command.trim()
      const sessionRequest = trimmed === "" || trimmed === "/bin/sh" ? { shell: "/bin/sh" } : { cmd: ["/bin/sh", "-lc", trimmed] }
      const session = await api.createSession(topology, node, sessionRequest)
      term.writeln(`created session ${session.id}; waiting for agent`)
      void watchSession(session.id, term)
      const ws = new WebSocket(sessionAttachURL(session.id))
      wsRef.current = ws
      ws.onopen = () => term.writeln(`attached session ${session.id}`)
      ws.onmessage = (event) => {
        let frame: { type: string; data?: string; code?: number; error?: string }
        try {
          frame = JSON.parse(event.data) as { type: string; data?: string; code?: number; error?: string }
        } catch {
          term.writeln(String(event.data))
          return
        }
        if (frame.type === "stdout" || frame.type === "stderr") {
          term.write(atob(frame.data || ""))
        } else if (frame.type === "exit") {
          term.writeln("")
          term.writeln(`exit ${frame.code ?? 0}`)
          setBusy(false)
        } else if (frame.type === "error") {
          writeLineOnce(term, frame.error || "session error")
          setBusy(false)
        }
      }
      ws.onerror = () => {
        writeLineOnce(term, "websocket error")
        setBusy(false)
      }
      ws.onclose = (event) => {
        if (event.code !== 1000 && event.reason) {
          writeLineOnce(term, `websocket closed: ${event.reason}`)
        }
        setBusy(false)
      }
      dataDisposableRef.current = term.onData((data) => {
        if (ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: "stdin", data: btoa(data) }))
        }
      })
    } catch (err) {
      term.writeln(err instanceof Error ? err.message : String(err))
      setBusy(false)
    }
  }

  async function watchSession(sessionID: string, term: Terminal) {
    for (let attempt = 0; attempt < 40; attempt++) {
      await new Promise((resolve) => window.setTimeout(resolve, 750))
      try {
        const session = await api.session(sessionID)
        if (session.status === "failed" || session.status === "denied" || session.status === "cancelled") {
          writeLineOnce(term, session.error || `session ${session.status}`)
          setBusy(false)
          return
        }
        if (session.status === "closed") {
          setBusy(false)
          return
        }
      } catch (err) {
        writeLineOnce(term, err instanceof Error ? err.message : String(err))
        setBusy(false)
        return
      }
    }
  }

  function writeLineOnce(term: Terminal, message: string) {
    if (printedMessagesRef.current.has(message)) {
      return
    }
    printedMessagesRef.current.add(message)
    term.writeln(message)
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
            <Button onClick={start} disabled={!node || !topology || busy || !terminalReady || nodeUnavailable}>
              {terminalReady ? "Start" : "Preparing"}
            </Button>
            {nodeUnavailable ? (
              <Button variant="outline" onClick={onRepair} disabled={!onRepair || busy}>
                Repair
              </Button>
            ) : null}
          </div>
          {nodeUnavailable ? (
            <p className="text-xs text-muted-foreground">
              Node is {nodeHealth?.reason || nodeHealth?.observation?.status || nodeHealth?.status}; repair it before opening a console.
            </p>
          ) : status ? (
            <p className="text-xs text-muted-foreground">{status}</p>
          ) : null}
          <div ref={setTerminalHost} className="h-96 overflow-hidden rounded-md border bg-slate-950" />
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
