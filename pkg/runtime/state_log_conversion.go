package runtime

import (
	"encoding/json"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/state"
)

func StateResourceFromLog(rec StateResourceLog) state.Resource {
	attributes := cloneInstance(rec.Instance)
	delete(attributes, "provider_extra")
	resource := state.Resource{
		ResourceType: rec.ResourceType, SchemaVersion: rec.SchemaVersion, OwnerUID: rec.OwnerUID, ResourceUID: rec.ResourceUID, Generation: rec.Generation, Dependencies: append([]address.Address(nil), rec.Dependencies...),
		Address:     address.Resource(rec.Type, rec.Name),
		Driver:      rec.Provider,
		ExternalID:  rec.ExternalID,
		Attributes:  state.MustAttributes(attributes),
		Attachments: cloneAttachments(rec.Attachments),
		Private:     append(json.RawMessage(nil), rec.Private...),
		Status:      rec.Status,
	}
	return resource
}

func AdoptStateResource(st *state.State, rec StateResourceLog, externalID string) {
	res := StateResourceFromLog(rec)
	if externalID != "" {
		switch rec.Type {
		case "sysbox_network":
			_ = res.SetAttribute("docker_network_id", externalID)
		case "sysbox_node", "sysbox_router":
			_ = res.SetAttribute("container_id", externalID)
		}
	}
	st.AddResource(res)
}

func cloneInstance(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
