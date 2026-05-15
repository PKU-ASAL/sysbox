package sink

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/oslab/sysbox/pkg/sensor"
)

// JSONLSink appends events as newline-delimited JSON to a file.
// The file is created (with parent dirs) on first write.
type JSONLSink struct {
	path string
	mu   sync.Mutex
	f    *os.File
	enc  *json.Encoder
}

// NewJSONLSink returns a sink that writes to path.
// The file is created lazily on the first Write call.
func NewJSONLSink(path string) *JSONLSink {
	return &JSONLSink{path: path}
}

func (s *JSONLSink) Write(e sensor.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.f == nil {
		if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
			return fmt.Errorf("create sink dir: %w", err)
		}
		f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("open sink file: %w", err)
		}
		s.f = f
		s.enc = json.NewEncoder(f)
	}
	return s.enc.Encode(e)
}

func (s *JSONLSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f != nil {
		return s.f.Close()
	}
	return nil
}
