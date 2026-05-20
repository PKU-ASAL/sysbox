package runtime

type AttributeSchema struct {
	Name            string
	Computed        bool
	Sensitive       bool
	RequiresReplace bool
}

type ResourceSchema struct {
	Type          string
	Attributes    map[string]AttributeSchema
	IgnoreChanges map[string]bool
}

func (s ResourceSchema) Attribute(name string) AttributeSchema {
	if attr, ok := s.Attributes[name]; ok {
		return attr
	}
	return AttributeSchema{Name: name, RequiresReplace: true}
}

func ResourceSchemaFor(typ string) ResourceSchema {
	s := ResourceSchema{
		Type:          typ,
		Attributes:    map[string]AttributeSchema{},
		IgnoreChanges: map[string]bool{"name": true, "type": true},
	}
	add := func(name string, computed, sensitive, replace bool) {
		s.Attributes[name] = AttributeSchema{
			Name:            name,
			Computed:        computed,
			Sensitive:       sensitive,
			RequiresReplace: replace,
		}
	}
	switch typ {
	case "sysbox_network":
		add("cidr", false, false, true)
		add("network_type", false, false, true)
		add("nat", false, false, true)
	case "sysbox_image":
		add("substrate", false, false, true)
		add("docker_ref", false, false, true)
		add("rootfs", false, false, true)
		add("qcow2", false, false, true)
		add("sha256", false, false, true)
		add("size", false, false, true)
	case "sysbox_kernel":
		add("substrate", false, false, true)
		add("source", false, false, true)
		add("sha256", false, false, true)
		add("cmdline_template", false, false, true)
		add("depends_on", false, false, true)
	case "sysbox_node":
		add("image", false, false, true)
		add("substrate", false, false, true)
		add("vcpus", false, false, true)
		add("memory", false, false, true)
		add("env", false, true, true)
		add("depends_on", false, false, true)
		add("links", false, false, true)
		add("routes", false, false, true)
		add("connections", false, true, true)
		add("provisioners", false, true, true)
		add("provider_config", false, true, true)
	case "sysbox_router":
		add("substrate", false, false, true)
		add("image", false, false, true)
		add("interfaces", false, false, true)
		add("nat_from", false, false, true)
		add("nat_to", false, false, true)
	case "sysbox_firewall":
		add("attach_to", false, false, true)
		add("rules", false, false, true)
	case "sysbox_ssh_access":
		add("node", false, false, true)
		add("authorized_keys", false, true, true)
		add("bind_ip", false, false, true)
		add("port", false, false, true)
	case "sysbox_actor":
		add("position", false, false, true)
		add("node", false, false, true)
		add("image", false, false, true)
		add("links", false, false, true)
		add("command", false, true, true)
		add("port", false, false, true)
		add("acp_ip", false, false, true)
		add("env", false, true, true)
		add("entry_points", false, false, true)
		add("depends_on", false, false, true)
	default:
		add("data", false, true, true)
	}
	return s
}
