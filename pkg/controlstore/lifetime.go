package controlstore

import "context"

// Validate opens the configured backend and checks its exact current schema.
func Validate(ctx context.Context, store Store) error {
	switch s := store.(type) {
	case *SQLite:
		_, err := s.OpenDB()
		return err
	case *Postgres:
		_, err := s.PoolFor(ctx)
		return err
	default:
		return nil
	}
}

// Close is called only after all consumers have joined their handlers.
func Close(store Store) error {
	switch s := store.(type) {
	case *SQLite:
		s.OpenMu.Lock()
		defer s.OpenMu.Unlock()
		if s.DB != nil {
			err := s.DB.Close()
			s.DB = nil
			return err
		}
	case *Postgres:
		s.Mu.Lock()
		defer s.Mu.Unlock()
		if s.Pool != nil {
			s.Pool.Close()
			s.Pool = nil
		}
	}
	return nil
}
