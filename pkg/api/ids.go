package api

import "strings"

func artifactID(name string) string {
	return prefixedID("art", name)
}

func topologyID(name string) string {
	return prefixedID("topo", name)
}

func prefixedID(prefix, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return prefix + "_unknown"
	}
	return prefix + "_" + name
}
