package model

type PlanFingerprint struct {
	ConfigSHA256    string            `json:"config_sha256"`
	StateLineage    string            `json:"state_lineage"`
	StateSerial     int64             `json:"state_serial"`
	ResourceSchemas map[string]int    `json:"resource_schemas"`
	Drivers         map[string]string `json:"drivers"`
	Artifacts       map[string]string `json:"artifacts"`
	VariablesSHA256 string            `json:"variables_sha256"`
}
