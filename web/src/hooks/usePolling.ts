import { useCallback, useEffect, useRef, useState } from "react"

type PollState<T> = {
  data?: T
  error?: string
  loading: boolean
}

export function usePolling<T>(load: () => Promise<T>, intervalMs = 5000) {
  const [state, setState] = useState<PollState<T>>({ loading: true })
  const active = useRef(true)
  const loadRef = useRef(load)
  const inFlight = useRef(false)

  useEffect(() => {
    loadRef.current = load
  }, [load])

  const refresh = useCallback(async () => {
    if (document.visibilityState === "hidden") {
      return
    }
    if (inFlight.current) {
      return
    }
    inFlight.current = true
    try {
      const data = await loadRef.current()
      if (active.current) {
        setState({ data, loading: false })
      }
    } catch (err) {
      if (active.current) {
        setState((prev) => ({ ...prev, loading: false, error: err instanceof Error ? err.message : String(err) }))
      }
    } finally {
      inFlight.current = false
    }
  }, [])

  useEffect(() => {
    active.current = true
    void refresh()
    const timer = window.setInterval(refresh, intervalMs)
    const onVisible = () => {
      if (document.visibilityState === "visible") {
        void refresh()
      }
    }
    document.addEventListener("visibilitychange", onVisible)
    return () => {
      active.current = false
      window.clearInterval(timer)
      document.removeEventListener("visibilitychange", onVisible)
    }
  }, [intervalMs, refresh])

  return { ...state, refresh }
}
