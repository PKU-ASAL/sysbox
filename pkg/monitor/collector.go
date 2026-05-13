package monitor

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/oslab/sysbox/pkg/sensor"
	"github.com/oslab/sysbox/pkg/sink"
)

// Collector fans in events from one or more Backend channels into a unified sink.
// Each channel is drained in its own goroutine; Run blocks until all channels
// are closed or ctx is cancelled.
type Collector struct {
	sink sink.EventSink
}

// NewCollector creates a Collector backed by the given EventSink.
func NewCollector(s sink.EventSink) *Collector {
	return &Collector{sink: s}
}

// Run starts one drain goroutine per channel and blocks until all goroutines
// finish (channels closed) or ctx is cancelled.
func (c *Collector) Run(ctx context.Context, channels ...<-chan sensor.Event) {
	var wg sync.WaitGroup
	for _, ch := range channels {
		ch := ch
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case ev, ok := <-ch:
					if !ok {
						return
					}
					if err := c.sink.Write(ev); err != nil {
						fmt.Fprintf(os.Stderr, "[monitor/collector] write: %v\n", err)
					}
				case <-ctx.Done():
					// Drain remaining buffered events before exiting.
					for {
						select {
						case ev, ok := <-ch:
							if !ok {
								return
							}
							_ = c.sink.Write(ev)
						default:
							return
						}
					}
				}
			}
		}()
	}
	wg.Wait()
}
