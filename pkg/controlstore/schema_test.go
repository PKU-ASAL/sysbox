package controlstore

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPISchemaHasNoMigrationLadder(t *testing.T) {
	require.NotContains(t, postgresSchema, "ALTER TABLE")
	require.NotContains(t, postgresSchema, "IF NOT EXISTS")
	for _, shape := range postgresTableShapes {
		require.Contains(t, postgresSchema, "CREATE TABLE "+shape.table+" (")
	}
}
