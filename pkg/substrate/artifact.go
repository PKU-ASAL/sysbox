package substrate

import (
	"fmt"
	"regexp"
)

type ArtifactKind string

const (
	ArtifactOCI       ArtifactKind = "oci"
	ArtifactRootFS    ArtifactKind = "rootfs"
	ArtifactQCow2     ArtifactKind = "qcow2"
	ArtifactRaw       ArtifactKind = "raw"
	ArtifactKernel    ArtifactKind = "kernel"
	ArtifactISO       ArtifactKind = "iso"
	ArtifactDriverISO ArtifactKind = "driver_iso"
)

var artifactDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func ValidateArtifactKind(kind ArtifactKind) error {
	switch kind {
	case ArtifactOCI, ArtifactRootFS, ArtifactQCow2, ArtifactRaw, ArtifactKernel, ArtifactISO, ArtifactDriverISO:
		return nil
	default:
		return fmt.Errorf("unsupported artifact kind %q", kind)
	}
}

type ArtifactIdentity struct {
	Kind         ArtifactKind
	Source       string
	Digest       string
	Architecture string
	GuestFamily  GuestFamily
	Metadata     map[string]string
}

func (i ArtifactIdentity) Validate() error {
	if err := ValidateArtifactKind(i.Kind); err != nil {
		return err
	}
	if i.Source == "" {
		return fmt.Errorf("artifact source is required")
	}
	if !artifactDigestPattern.MatchString(i.Digest) {
		return fmt.Errorf("artifact digest must be sha256:<64 lowercase hex characters>")
	}
	if i.Architecture == "" {
		return fmt.Errorf("artifact architecture is required")
	}
	return ValidateGuestFamily(i.GuestFamily)
}

func (i ArtifactIdentity) Clone() ArtifactIdentity {
	i.Metadata = cloneStringMap(i.Metadata)
	return i
}
