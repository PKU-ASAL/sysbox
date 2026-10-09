package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/persistence"
)

// Command updates and dispatch creation share the run-record lock across
// processes. Standalone records override the immutable dispatch seed; a
// retired record must remain to prevent that seed from being delivered again.
type localCommandStorage struct{ store *localAPIStore }

func (b localCommandStorage) records(ctx context.Context, agent string) ([]persistence.CommandRecord, error) {
	scope := agent
	if scope == "" {
		scope = "*"
	}
	files, err := localRecordPaths(filepath.Join(b.store.runsDir, "_agents", scope, "commands", "*.json"))
	if err != nil {
		return nil, err
	}
	byID := map[string]persistence.CommandRecord{}
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		record, err := persistence.DecodeCommandRecord(raw)
		if err != nil {
			return nil, err
		}
		if filepath.Base(path) != record.Command.ID+".json" || filepath.Base(filepath.Dir(filepath.Dir(path))) != record.Command.AgentID {
			return nil, persistence.ErrCorruptRecord
		}
		if _, ok := byID[record.Command.ID]; ok {
			return nil, persistence.ErrCorruptRecord
		}
		byID[record.Command.ID] = *record
	}
	dispatches, err := b.store.loadRunDispatches()
	if err != nil {
		return nil, err
	}
	guestCommands, err := persistence.NewFilesystemGuestRepository(b.store.runsDir).FilesystemGuestDispatchCommands(ctx)
	if err != nil {
		return nil, err
	}
	consoleCommands, err := b.store.consoleDispatchSeeds(ctx)
	if err != nil {
		return nil, err
	}
	guestCommands = append(guestCommands, consoleCommands...)
	seeds := map[string]bool{}
	for _, record := range guestCommands {
		if seeds[record.Command.ID] {
			return nil, persistence.ErrCorruptRecord
		}
		seeds[record.Command.ID] = true
		if agent == "" || record.Command.AgentID == agent {
			if current, exists := byID[record.Command.ID]; exists {
				if current.Command.AgentID != record.Command.AgentID {
					return nil, persistence.ErrCorruptRecord
				}
			} else {
				byID[record.Command.ID] = record
			}
		}
	}
	for _, dispatch := range dispatches {
		if dispatch.RequestOnly {
			continue
		}
		if agent != "" && dispatch.Command.AgentID != agent {
			continue
		}
		raw, err := persistence.EncodeCommandRecord(persistence.CommandRecord{Command: dispatch.Command})
		if err != nil {
			return nil, err
		}
		record, err := persistence.DecodeCommandRecord(raw)
		if err != nil {
			return nil, err
		}
		if seeds[record.Command.ID] {
			return nil, persistence.ErrCorruptRecord
		}
		seeds[record.Command.ID] = true
		if current, ok := byID[record.Command.ID]; ok {
			if current.Command.AgentID != record.Command.AgentID {
				return nil, persistence.ErrCorruptRecord
			}
		} else {
			byID[record.Command.ID] = *record
		}
	}
	records := make([]persistence.CommandRecord, 0, len(byID))
	for _, record := range byID {
		records = append(records, record)
	}
	return records, nil
}
func (b localCommandStorage) read(ctx context.Context, id string) (*persistence.CommandRecord, error) {
	files, err := localRecordPaths(filepath.Join(b.store.runsDir, "_agents", "*", "commands", id+".json"))
	if err != nil {
		return nil, err
	}
	if len(files) > 1 {
		return nil, persistence.ErrCorruptRecord
	}
	if len(files) == 1 {
		raw, err := os.ReadFile(files[0])
		if err != nil {
			return nil, err
		}
		record, err := persistence.DecodeCommandRecord(raw)
		if err != nil {
			return nil, err
		}
		if record.Command.ID != id || record.Command.AgentID != filepath.Base(filepath.Dir(filepath.Dir(files[0]))) {
			return nil, persistence.ErrCorruptRecord
		}
		return record, nil
	}
	dispatches, err := b.store.loadRunDispatches()
	if err != nil {
		return nil, err
	}
	var current *persistence.CommandRecord
	for _, dispatch := range dispatches {
		if dispatch.RequestOnly {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if dispatch.Command.ID != id {
			continue
		}
		if current != nil {
			return nil, persistence.ErrCorruptRecord
		}
		raw, err := persistence.EncodeCommandRecord(persistence.CommandRecord{Command: dispatch.Command})
		if err != nil {
			return nil, err
		}
		current, err = persistence.DecodeCommandRecord(raw)
		if err != nil {
			return nil, err
		}
	}
	guestCommands, err := persistence.NewFilesystemGuestRepository(b.store.runsDir).FilesystemGuestDispatchCommands(ctx)
	if err != nil {
		return nil, err
	}
	consoleCommands, err := b.store.consoleDispatchSeeds(ctx)
	if err != nil {
		return nil, err
	}
	guestCommands = append(guestCommands, consoleCommands...)
	for _, record := range guestCommands {
		if record.Command.ID != id {
			continue
		}
		if current != nil {
			return nil, persistence.ErrCorruptRecord
		}
		copy := record
		current = &copy
	}
	return current, nil
}
func (b localCommandStorage) ReadCommand(ctx context.Context, id string) (*persistence.CommandRecord, error) {
	unlock, err := b.store.lockRunRecords(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return b.read(ctx, id)
}
func (b localCommandStorage) ListCommands(ctx context.Context, agent string) ([]persistence.CommandRecord, error) {
	unlock, err := b.store.lockRunRecords(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return b.records(ctx, agent)
}
func (b localCommandStorage) UpdateCommand(ctx context.Context, id string, transform func(*persistence.CommandRecord) (*persistence.CommandRecord, error)) (*persistence.CommandRecord, error) {
	unlock, err := b.store.lockRunRecords(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	current, err := b.read(ctx, id)
	if err != nil {
		return nil, err
	}
	next, err := transform(current)
	if err != nil {
		return nil, err
	}
	if next == nil {
		return current, nil
	}
	raw, err := persistence.EncodeCommandRecord(*next)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(b.store.runsDir, "_agents", next.Command.AgentID, "commands", id+".json")
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := writeLocalObject(path, json.RawMessage(raw)); err != nil {
		return nil, err
	}
	return next, nil
}
