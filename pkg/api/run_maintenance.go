package api

import (
	"github.com/oslab/sysbox/pkg/persistence"
)

func validateRunAuthority(raw []byte, uid string) error {
	return persistence.ValidateMaintenanceWrite(raw, uid)
}
