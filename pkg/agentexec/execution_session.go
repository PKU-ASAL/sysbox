package agentexec

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/controlplane"
)

type executionSessionRecord struct {
	Version            int                       `json:"version"`
	Session            controlplane.AgentSession `json:"session"`
	PendingIncarnation string                    `json:"pending_incarnation,omitempty"`
}

// OpenExecutionSession lets an embedded credential-owning controller use the
// same durable registration/recovery protocol over its own HTTP transport.
func OpenExecutionSession(ctx context.Context, opts Options, client *http.Client) (controlplane.AgentSession, error) {
	opts.httpClient = client
	return establishExecutionSession(ctx, opts)
}

// Persist the registration nonce before making a request. A lost response is
// recovered using that same nonce before rotating to the new process identity.
func establishExecutionSession(ctx context.Context, opts Options) (controlplane.AgentSession, error) {
	if opts.Secret == "" || opts.ExecutionSessionFile == "" {
		return controlplane.AgentSession{}, errors.New("execution authority requires an Agent secret and durable session file")
	}
	path := opts.ExecutionSessionFile
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return controlplane.AgentSession{}, err
	}
	lock := flock.New(path + ".lock")
	defer lock.Close()
	locked, err := lock.TryLockContext(ctx, 5*time.Millisecond)
	if err != nil {
		return controlplane.AgentSession{}, err
	}
	if !locked {
		return controlplane.AgentSession{}, ctx.Err()
	}
	record := executionSessionRecord{Version: 1}
	raw, err := os.ReadFile(path)
	if err == nil {
		if json.Unmarshal(raw, &record) != nil || record.Version != 1 {
			return controlplane.AgentSession{}, errors.New("unsupported execution session record; explicitly rebuild")
		}
		if record.Session.ID != "" && record.Session.AgentID != opts.ID {
			return controlplane.AgentSession{}, errors.New("execution session belongs to another Agent")
		}
	} else if !os.IsNotExist(err) {
		return controlplane.AgentSession{}, err
	}
	register := func() error {
		var session controlplane.AgentSession
		err := post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/execution-session", map[string]string{"incarnation_id": record.PendingIncarnation, "expected_session": record.Session.ID}, &session)
		if err != nil {
			return err
		}
		if session.AgentID != opts.ID || session.ID == "" || session.IncarnationID != record.PendingIncarnation {
			return errors.New("invalid execution session response")
		}
		record.Session = session
		record.PendingIncarnation = ""
		return writeExecutionSession(path, record)
	}
	if record.PendingIncarnation != "" {
		if err = register(); err != nil {
			return controlplane.AgentSession{}, err
		}
	}
	record.PendingIncarnation = uuid.NewString()
	if err = writeExecutionSession(path, record); err != nil {
		return controlplane.AgentSession{}, err
	}
	if err = register(); err != nil {
		return controlplane.AgentSession{}, err
	}
	return record.Session, nil
}

func writeExecutionSession(path string, record executionSessionRecord) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".session-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
