package docker

import "testing"

const (
	configDigest   = "sha256:d19bacb84eae88b58cb0f5a6fcd3d648e5a16fa149c219100153f1f73dc52a5e"
	manifestDigest = "sha256:990e83490108b686fd6df3b1cafa6bdbb2714acb00eedb9a89693946f46f45ce"
)

func TestImageDigestCandidatesIncludesIDAndRepoDigests(t *testing.T) {
	got := imageDigestCandidates(configDigest, []string{
		"ghcr.io/pku-asal/frrouting/frr@" + manifestDigest,
		"no-at-sign-entry",
	})
	if len(got) != 2 || got[0] != configDigest || got[1] != manifestDigest {
		t.Fatalf("unexpected candidates: %v", got)
	}
}

func TestDigestMatchesAcceptsEitherDaemonIdentity(t *testing.T) {
	docker2010 := imageDigestCandidates(configDigest, []string{"repo/img@" + manifestDigest})
	docker29 := imageDigestCandidates(manifestDigest, []string{"repo/img@" + manifestDigest})
	if !digestMatches(docker2010, configDigest) {
		t.Fatal("config-digest lock must match on Docker 20.10")
	}
	if !digestMatches(docker2010, manifestDigest) {
		t.Fatal("manifest-digest lock must match via repo digests on Docker 20.10")
	}
	if !digestMatches(docker29, manifestDigest) {
		t.Fatal("manifest-digest lock must match on Docker 29")
	}
	// The config digest is invisible to Docker 29's inspect; scenario pins that
	// must verify on both daemon generations have to use the manifest digest.
	if digestMatches(docker29, configDigest) {
		t.Fatal("config digest must not accidentally match on Docker 29")
	}
	if digestMatches(docker29, "sha256:0000") {
		t.Fatal("unrelated digest must not match")
	}
}

func TestNormalizeDigest(t *testing.T) {
	bare := "d19bacb84eae88b58cb0f5a6fcd3d648e5a16fa149c219100153f1f73dc52a5e"
	if got := normalizeDigest(bare); got != configDigest {
		t.Fatalf("normalizeDigest(%q) = %q", bare, got)
	}
	if got := normalizeDigest(""); got != "" {
		t.Fatalf("empty digest must stay empty, got %q", got)
	}
}
