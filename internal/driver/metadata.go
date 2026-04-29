package driver

// Metadata is the driver-local metadata surface for node and cluster facts that
// CSI RPC handlers need. Today the scaffold only needs a stable NodeID, but this
// will likely grow once the node plugin needs topology, mount, or cluster state.
type Metadata struct {
	NodeID string
}

// newMetadata builds the current metadata surface from environment-derived input.
// Future implementations can replace this with a richer metadata loader once the
// file storage backend and node requirements are defined.
func newMetadata(nodeName string) Metadata {
	if nodeName == "" {
		return Metadata{NodeID: "linode-filestorage-node"}
	}
	return Metadata{NodeID: nodeName}
}
