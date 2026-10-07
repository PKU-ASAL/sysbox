package guesttask

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type scopeRecord struct {
	Version int    `json:"version"`
	Epoch   uint64 `json:"epoch"`
	Session string `json:"session"`
}

func (e *Engine) lockScope(ctx context.Context, grant controlplane.ExecutionGrant) (string, scopeRecord, func(), error) {
	name := fmt.Sprintf("scope-%x", sha256.Sum256([]byte(grant.Scope.TopologyUID+"\x00"+grant.Scope.ResourceUID)))
	scopes := Engine{Root: filepath.Join(e.Root, ".scopes")}
	unlock, err := scopes.lock(ctx, name)
	if err != nil {
		return "", scopeRecord{}, nil, err
	}
	path := filepath.Join(scopes.Root, name+".json")
	var record scopeRecord
	err = readJSON(path, &record)
	if err != nil && !os.IsNotExist(err) {
		unlock()
		return "", record, nil, err
	}
	if os.IsNotExist(err) {
		record.Version = 2
	}
	if err == nil && (record.Version != 2 || record.Epoch == 0 || record.Session == "") {
		unlock()
		return "", record, nil, fmt.Errorf("guest scope version requires explicit rebuild")
	}
	if grant.Epoch < record.Epoch || grant.Epoch == record.Epoch && record.Session != "" && record.Session != grant.SessionID {
		unlock()
		return "", record, nil, ErrUnauthorized
	}
	return path, record, unlock, nil
}
func sameScope(a, b controlplane.ExecutionScope) bool {
	return a.TopologyUID == b.TopologyUID && a.ResourceUID == b.ResourceUID
}
func (e *Engine) scopeQuiescent(grant controlplane.ExecutionGrant) error {
	tasks, err := e.listUnlocked()
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if sameScope(task.Grant.Grant.Scope, grant.Scope) && !task.TerminationConfirmed {
			return fmt.Errorf("previous guest task is not quiescent")
		}
	}
	files, err := e.listInstalls()
	if err != nil {
		return err
	}
	for _, file := range files {
		if sameScope(file.Grant.Grant.Scope, grant.Scope) && !file.Result.Quiescent {
			return fmt.Errorf("previous guest file install is not quiescent")
		}
	}
	return nil
}
