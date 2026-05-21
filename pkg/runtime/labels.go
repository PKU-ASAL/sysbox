package runtime

import "github.com/oslab/sysbox/pkg/graph"

const (
	LabelManaged      = "sysbox.managed"
	LabelTopology     = "sysbox.topology"
	LabelRunID        = "sysbox.run_id"
	LabelResource     = "sysbox.resource"
	LabelResourceType = "sysbox.resource_type"
	LabelResourceName = "sysbox.resource_name"
)

func ManagedLabels(topology, runID string, id graph.NodeID) map[string]string {
	labels := map[string]string{
		LabelManaged:      "true",
		LabelResource:     id.String(),
		LabelResourceType: id.Type,
		LabelResourceName: id.Name,
	}
	if topology != "" {
		labels[LabelTopology] = topology
	}
	if runID != "" {
		labels[LabelRunID] = runID
	}
	return labels
}
