package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/revisiongate"
)

type revisionFederationRequest struct {
	Registry  string    `json:"registry"`
	Request   string    `json:"request"`
	Revisions []string  `json:"revisions"`
	OlderThan time.Time `json:"older_than"`
}

func federationToken(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return nil, fmt.Errorf("federation token must be private regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) < 8 {
		return nil, fmt.Errorf("invalid federation token")
	}
	return raw, nil
}
func (s *Server) handleFederatedRetirement(w http.ResponseWriter, r *http.Request) {
	var input revisionFederationRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		writeError(w, 400, persistence.ErrInvalidRecord)
		return
	}
	path, ok := s.cfg.RevisionUpstreams[input.Registry]
	if !ok {
		writeError(w, 401, persistence.ErrConflict)
		return
	}
	token, err := federationToken(path)
	if err != nil {
		writeError(w, 503, fmt.Errorf("federation authentication unavailable"))
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")), token) != 1 {
		writeError(w, 401, persistence.ErrConflict)
		return
	}
	repo, err := persistence.OpenCatalog(r.Context(), s.stateBackend)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	defer repo.Close()
	if err = repo.RetireFederatedRevisions(r.Context(), input.Registry, input.Request, input.Revisions, input.OlderThan); err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]any{"retired": input.Revisions, "request": input.Request})
}

// Caller holds its own registry lock throughout downstream retirement and local
// commit. Consumer retirement is deliberately irreversible; partial transport
// failure retains durable intent and retries until every consumer acknowledges.
func (s *Server) retireSQLConsumers(ctx context.Context, root string, ids []string, olderThan time.Time) error {
	consumers, err := s.enrolledSQLConsumers(root)
	if err != nil {
		return err
	}
	if len(consumers) == 0 {
		return nil
	}
	raw, _ := json.Marshal(struct {
		IDs    []string
		Cutoff time.Time
	}{ids, olderThan})
	request := fmt.Sprintf("federated-%x", sha256.Sum256(raw))
	input := revisionFederationRequest{Registry: s.cfg.RevisionRegistryID, Request: request, Revisions: ids, OlderThan: olderThan}
	if input.Registry == "" {
		return fmt.Errorf("revision_registry_id required")
	}
	if err := writeLocalObjectWithoutGate(filepath.Join(root, request+".intent"), input); err != nil {
		return err
	}
	names := make([]string, 0, len(consumers))
	for name := range consumers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		consumer := consumers[name]
		if err := revisiongate.PostRetirement(ctx, consumer.URL, consumer.TokenFile, input); err != nil {
			return fmt.Errorf("consumer %s retirement pending: %w", name, err)
		}
	}
	return nil
}
func writeLocalObjectWithoutGate(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return publishLocalObject(path, raw)
}

func (s *Server) enrolledSQLConsumers(root string) (map[string]config.RevisionSQLConsumer, error) {
	return enrollSQLConsumers(s.cfg, root)
}

func enrollSQLConsumers(cfg config.ServiceConfig, root string) (map[string]config.RevisionSQLConsumer, error) {
	path := filepath.Join(root, ".sql-consumers.json")
	anchor := filepath.Join(filepath.Dir(root), ".sysbox-sql-consumers.required")
	raw, err := os.ReadFile(path)
	exists := err == nil
	if os.IsNotExist(err) {
		if _, e := os.Lstat(anchor); !os.IsNotExist(e) {
			return nil, fmt.Errorf("SQL consumer enrollment evidence lost")
		}
		if len(cfg.RevisionSQLConsumers) == 0 {
			return nil, nil
		}
	} else if err != nil {
		return nil, err
	}
	type enrolled struct {
		Registry  string                                `json:"registry"`
		Consumers map[string]config.RevisionSQLConsumer `json:"consumers"`
	}
	prior := enrolled{}
	if exists {
		if json.Unmarshal(raw, &prior) != nil || prior.Consumers == nil || prior.Registry != cfg.RevisionRegistryID {
			return nil, fmt.Errorf("SQL consumer enrollment changed")
		}
	}
	for id, old := range prior.Consumers {
		current, ok := cfg.RevisionSQLConsumers[id]
		if !ok || old != current {
			return nil, fmt.Errorf("SQL consumer %s missing or changed", id)
		}
	}
	if len(cfg.RevisionSQLConsumers) > 0 {
		if cfg.RevisionRegistryID == "" {
			return nil, fmt.Errorf("registry ID required for SQL federation")
		}
		if err = writeLocalObjectWithoutGate(path, enrolled{Registry: cfg.RevisionRegistryID, Consumers: cfg.RevisionSQLConsumers}); err != nil {
			return nil, err
		}
		if err = writeLocalObjectWithoutGate(anchor, map[string]string{"catalog": root}); err != nil {
			return nil, err
		}
	}
	return cfg.RevisionSQLConsumers, nil
}
