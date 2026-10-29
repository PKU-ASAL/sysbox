package controlstore

import (
	"crypto/ed25519"

	"github.com/oslab/sysbox/pkg/persistence"
)

type Transactions struct {
	RunsDir          string
	Store            Store
	ActionRepository func(string) (*persistence.Repository, error)
	GrantHostKeys    map[string]ed25519.PublicKey
	StateBackend     string
}
