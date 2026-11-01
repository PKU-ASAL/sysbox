package controlstore

import (
	"context"

	"github.com/oslab/sysbox/pkg/persistence"
)

// grantTransaction lets the shared state machine operate on bytes already
// protected by the API's compound authority/command transaction.
type GrantTransaction struct{ Raw []byte }

func (t *GrantTransaction) ReadExecutionAuthority(context.Context) ([]byte, error) {
	if t.Raw == nil {
		return nil, persistence.ErrNotFound
	}
	return append([]byte(nil), t.Raw...), nil
}

func (t *GrantTransaction) UpdateExecutionAuthority(_ context.Context, fn func([]byte) ([]byte, error)) error {
	raw, err := fn(t.Raw)
	if err == nil {
		t.Raw = raw
	}
	return err
}
