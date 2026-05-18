package api

import (
	"fmt"
	"net/http"
	"sync"
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
func (b *Broadcaster) Subscribe() <-chan string {
	ch := make(chan string, 128)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()
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
// The caller must have already set Content-Type: text/event-stream.
func ServeSSE(w http.ResponseWriter, ch <-chan string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	for line := range ch {
		fmt.Fprintf(w, "data: %s\n\n", line)
		flusher.Flush()
	}
}
