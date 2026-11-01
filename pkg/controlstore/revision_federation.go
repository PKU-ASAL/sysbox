package controlstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/config"
)

func WriteLocalObjectWithoutGate(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return publishLocalObject(path, raw)
}

func EnrollSQLConsumers(cfg config.ServiceConfig, root string) (map[string]config.RevisionSQLConsumer, error) {
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
		if err = WriteLocalObjectWithoutGate(path, enrolled{Registry: cfg.RevisionRegistryID, Consumers: cfg.RevisionSQLConsumers}); err != nil {
			return nil, err
		}
		if err = WriteLocalObjectWithoutGate(anchor, map[string]string{"catalog": root}); err != nil {
			return nil, err
		}
	}
	return cfg.RevisionSQLConsumers, nil
}
