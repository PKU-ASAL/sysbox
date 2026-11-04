package buildinfo

import (
	"strings"
	"testing"
)

func TestCheckCompatibleRequiresExactKnownBuild(t *testing.T) {
	good := Info{Version: "v0.1.0", Commit: strings.Repeat("a", 40)}
	for _, tc := range []struct {
		name string
		peer Info
		ok   bool
	}{
		{"matching", good, true},
		{"different commit", Info{Version: good.Version, Commit: strings.Repeat("b", 40)}, false},
		{"different version", Info{Version: "v0.2.0", Commit: good.Commit}, false},
		{"missing", Info{}, false},
		{"unknown", Info{Version: "dev", Commit: "unknown"}, false},
		{"short commit", Info{Version: good.Version, Commit: "aaaaaaa"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckCompatible(good, tc.peer); (err == nil) != tc.ok {
				t.Fatalf("unexpected compatibility: %v", err)
			}
		})
	}
}
func TestUnstampedDevelopmentBuildIsExplicit(t *testing.T) {
	dev := Info{Version: "dev", Commit: "unknown"}
	if err := CheckCompatible(dev, dev); err != nil {
		t.Fatal(err)
	}
	if err := CheckCompatible(dev, Info{}); err == nil {
		t.Fatal("missing peer is not a development build")
	}
}
