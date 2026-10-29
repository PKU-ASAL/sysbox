package commands

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/oslab/sysbox/pkg/persistence"
	"net/url"
	"strings"
)

func gcCatalogLocation(value string) (string, string, error) {
	u, err := url.Parse(value)
	if err != nil {
		return "", "", err
	}
	if u.Scheme != "sqlite" && u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", "", fmt.Errorf("GC catalog requires shared SQLite or PostgreSQL")
	}
	if strings.Contains(u.Path, "{topology}") || strings.Contains(u.Host, "{topology}") {
		return "", "", fmt.Errorf("templated storage path is not a complete catalog")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", "", err
	}
	if selected := q.Get("topology"); selected != "" && selected != "{topology}" {
		return "", "", fmt.Errorf("fixed topology selector is not a full catalog")
	}
	q.Del("topology")
	u.RawQuery = q.Encode()
	location := u.String()
	u.User = nil
	q.Del("password")
	q.Del("passfile")
	u.RawQuery = q.Encode()
	return location, fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(u.String()))), nil
}
func collectCatalogIDs(ctx context.Context, location string) (activeIDs, error) {
	active := activeIDs{topologies: map[string]string{}, resourceUIDs: map[string]bool{}, owners: map[string]bool{}, inventories: map[string]string{}, containers: map[string]bool{}, networks: map[string]bool{}, domains: map[string]bool{}, firecracker: map[string]bool{}}
	backend, _, err := gcCatalogLocation(location)
	if err != nil {
		return active, err
	}
	repo, err := persistence.OpenCatalogSource(ctx, backend, "")
	if err != nil {
		return active, err
	}
	defer repo.Close()
	names, err := repo.ListTopologies(ctx)
	if err != nil {
		return active, err
	}
	if len(names) > 10000 {
		return active, fmt.Errorf("catalog exceeds 10000 topology limit")
	}
	for _, entry := range names {
		refs, err := repo.LoadGCReferences(ctx, entry.Name)
		if err != nil {
			return active, err
		}
		if active.owners[refs.TopologyUID] {
			return active, fmt.Errorf("duplicate topology owner")
		}
		active.owners[refs.TopologyUID] = true
		active.topologies[refs.TopologyUID] = entry.Name
		for domain, state := range refs.Domains {
			if state.Status != "complete" && state.Status != "not_applicable" {
				return active, fmt.Errorf("incomplete %s reference domain for %s", domain, entry.Name)
			}
		}
		active.inventories[refs.TopologyUID] = refs.Digest
		for _, ref := range refs.References {
			active.resourceUIDs[refs.TopologyUID+"\x00"+ref.ResourceUID] = true
			switch ref.ResourceType {
			case "sysbox_network":
				active.networks[ref.ExternalID] = true
			case "sysbox_node", "sysbox_router":
				switch ref.Driver {
				case "docker":
					active.containers[ref.ExternalID] = true
				case "libvirt":
					active.domains[ref.ExternalID] = true
				case "firecracker":
					active.firecracker[ref.ExternalID] = true
				}
			}
		}
	}
	return active, nil
}
