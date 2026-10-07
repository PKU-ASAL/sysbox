package substrate

// ManagedBridgeTarget is the immutable host link incarnation exported by a
// network provider for attachment by another substrate. It is not permission
// to adopt a bridge found by name; the provider must verify its durable anchor.
type ManagedBridgeTarget struct {
	Version   int               `json:"version"`
	Provider  string            `json:"provider"`
	NetworkID string            `json:"network_id"`
	Identity  OperationIdentity `json:"identity"`
	Device    uint64            `json:"device"`
	Inode     uint64            `json:"inode"`
	Bridge    string            `json:"bridge"`
	Index     int               `json:"index"`
	Alias     string            `json:"alias"`
}
