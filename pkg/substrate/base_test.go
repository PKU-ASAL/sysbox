package substrate_test

import (
	"errors"
	"testing"

	"github.com/oslab/sysbox/pkg/substrate"
)

func TestValidationError_TypeAssertion(t *testing.T) {
	err := substrate.NewValidationError("bad %s", "spec")
	if err.Error() != "bad spec" {
		t.Fatalf("unexpected message: %q", err.Error())
	}
	if !substrate.IsValidationError(err) {
		t.Fatal("IsValidationError should return true for *ValidationError")
	}
	if substrate.IsValidationError(errors.New("plain")) {
		t.Fatal("IsValidationError should return false for a plain error")
	}
}

func TestBaseSubstrate_Defaults(t *testing.T) {
	type embedder struct{ substrate.BaseSubstrate }
	var s embedder

	if err := s.Validate(substrate.NodeSpec{}); err != nil {
		t.Fatalf("BaseSubstrate.Validate should accept any spec by default, got %v", err)
	}

	v, err := s.DecodeProviderConfig(nil, nil)
	if err != nil {
		t.Fatalf("BaseSubstrate.DecodeProviderConfig should return nil error, got %v", err)
	}
	if v != nil {
		t.Fatalf("BaseSubstrate.DecodeProviderConfig should return nil value, got %v", v)
	}
}
