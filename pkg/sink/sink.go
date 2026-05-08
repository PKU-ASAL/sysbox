// Package sink provides event persistence backends.
package sink

import "github.com/oslab/sysbox/pkg/sensor"

// EventSink persists sensor events.
type EventSink interface {
	Write(e sensor.Event) error
	Close() error
}
