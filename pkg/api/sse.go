package api

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Broadcaster is an io.Writer that fans out written lines to all
// current SSE subscribers. Close() signals that no more data will come.
type Broadcaster struct {
	mu   sync.Mutex
	subs []chan string
	done bool
}

// Write implements io.Writer. Each call may contain multiple newline-separated
// lines; each line is broadcast individually.
func (b *Broadcaster) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done || len(b.subs) == 0 {
		return len(p), nil
	}
	line := string(p)
	for _, ch := range b.subs {
		select {
		case ch <- line:
		default: // slow consumer: drop rather than block the apply goroutine
		}
	}
	return len(p), nil
}

// Subscribe returns a channel that receives log lines until Close() is called.
// If the broadcaster is already closed, it returns an already-closed channel
// so the caller won't hang.
func (b *Broadcaster) Subscribe() <-chan string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		// Already closed: return a closed channel so the SSE handler exits immediately.
		ch := make(chan string)
		close(ch)
		return ch
	}
	ch := make(chan string, 128)
	b.subs = append(b.subs, ch)
	return ch
}

// Unsubscribe removes a subscriber channel.
func (b *Broadcaster) Unsubscribe(ch <-chan string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, s := range b.subs {
		if s == ch {
			b.subs = append(b.subs[:i], b.subs[i+1:]...)
			return
		}
	}
}

// Close signals that the run is finished; all subscriber channels are closed.
func (b *Broadcaster) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return
	}
	b.done = true
	for _, ch := range b.subs {
		close(ch)
	}
	b.subs = nil
}

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
