import { useCallback, useEffect, useRef, useState } from "react"

type PollState<T> = {
  data?: T
  error?: string
  loading: boolean
}

export function usePolling<T>(load: () => Promise<T>, intervalMs = 5000) {
  const [state, setState] = useState<PollState<T>>({ loading: true })
  const active = useRef(true)

  const refresh = useCallback(async () => {
    try {
      const data = await load()
      if (active.current) {
        setState({ data, loading: false })
      }
    } catch (err) {
      if (active.current) {
        setState((prev) => ({ ...prev, loading: false, error: err instanceof Error ? err.message : String(err) }))
      }
    }
  }, [load])

  useEffect(() => {
    active.current = true
    void refresh()
    const timer = window.setInterval(refresh, intervalMs)
    return () => {
      active.current = false
      window.clearInterval(timer)
    }
  }, [intervalMs, refresh])

  return { ...state, refresh }
}

