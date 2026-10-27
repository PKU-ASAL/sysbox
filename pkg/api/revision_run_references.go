package api

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) revisionRunReferences(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	count, total := 0, 0
	add := func(revision string) error {
		count++
		total += len(revision)
		if count > 100000 || total > maxRevisionInventoryBytes {
			return fmt.Errorf("run reference budget exceeded")
		}
		if revision != "" {
			out[revision] = true
		}
		return nil
	}
	switch store := s.apiStore.(type) {
	case *sqliteAPIStore:
		db, err := store.open()
		if err != nil {
			return nil, err
		}
		rows, err := db.QueryContext(ctx, "SELECT COALESCE(revision,'') FROM sysbox_runs ORDER BY id,rowid LIMIT 100001")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var revision string
			if err = rows.Scan(&revision); err != nil {
				return nil, err
			}
			if err = add(revision); err != nil {
				return nil, err
			}
		}
		if err = rows.Err(); err != nil {
			return nil, err
		}
	case *postgresAPIStore:
		conn, err := store.connect(ctx)
		if err != nil {
			return nil, err
		}
		defer conn.Release()
		rows, err := conn.Query(ctx, "SELECT COALESCE(data->>'revision','') FROM sysbox_runs ORDER BY id LIMIT 100001")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var revision string
			if err = rows.Scan(&revision); err != nil {
				return nil, err
			}
			if err = add(revision); err != nil {
				return nil, err
			}
		}
		if err = rows.Err(); err != nil {
			return nil, err
		}
	case *localAPIStore:
		remaining := int64(maxRevisionInventoryBytes)
		for _, pattern := range []string{filepath.Join(store.runsDir, "*", "runs.jsonl"), filepath.Join(store.runsDir, "_run_requests", "*.json")} {
			paths, err := localRecordPaths(pattern)
			if err != nil {
				return nil, err
			}
			if len(paths) > 10000 {
				return nil, persistence.ErrConflict
			}
			for _, path := range paths {
				if err = ctx.Err(); err != nil {
					return nil, err
				}
				info, err := os.Lstat(path)
				if err != nil {
					return nil, err
				}
				if !info.Mode().IsRegular() || info.Size() > remaining {
					return nil, persistence.ErrConflict
				}
				f, err := os.Open(path)
				if err != nil {
					return nil, err
				}
				raw, err := io.ReadAll(io.LimitReader(f, remaining+1))
				closeErr := f.Close()
				if err != nil {
					return nil, err
				}
				if closeErr != nil {
					return nil, closeErr
				}
				remaining -= int64(len(raw))
				if remaining < 0 {
					return nil, persistence.ErrConflict
				}
				if strings.HasSuffix(path, ".jsonl") {
					scanner := bufio.NewScanner(strings.NewReader(string(raw)))
					scanner.Buffer(make([]byte, 4096), maxRevisionInventoryBytes)
					for scanner.Scan() {
						line := scanner.Bytes()
						if len(line) == 0 {
							continue
						}
						var run controlplane.Run
						if err = decodeLocalRecord(line, &run); err != nil {
							return nil, err
						}
						if err = add(run.Revision); err != nil {
							return nil, err
						}
					}
					if err = scanner.Err(); err != nil {
						return nil, err
					}
				} else {
					var dispatch localRunDispatch
					if err = decodeLocalRecord(raw, &dispatch); err != nil {
						return nil, err
					}
					if err = add(dispatch.Run.Revision); err != nil {
						return nil, err
					}
				}
			}
		}
	default:
		return nil, persistence.ErrUnsupportedVersion
	}
	return out, nil
}
