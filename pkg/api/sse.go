package api

import (
	"fmt"
	"net/http"
	"time"
)

// ServeSSE writes an SSE stream from ch to w until ch is closed.
// Monitors the HTTP request context for client disconnect and exits
// promptly, preventing goroutine leaks.
// The caller must have already set Content-Type: text/event-stream.
func ServeSSE(w http.ResponseWriter, r *http.Request, ch <-chan string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return // channel closed, run is done
			}
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		case <-ctx.Done():
			// Client disconnected; stop streaming.
			return
		case <-ticker.C:
			// SSE keep-alive comment to detect dead connections.
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
