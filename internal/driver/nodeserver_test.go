package driver

import (
	"context"
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
)

type staticMetadataService struct {
	currentNode NodeMetadata
	err         error
}

func (s staticMetadataService) CurrentNode(ctx context.Context) (NodeMetadata, error) {
	return s.currentNode, s.err
}

func (s staticMetadataService) NodeByName(ctx context.Context, name string) (NodeMetadata, error) {
	return NodeMetadata{}, nil
}

func (s staticMetadataService) NodesByName(ctx context.Context, names []string) ([]NodeMetadata, error) {
	return nil, nil
}

func (s staticMetadataService) Cluster(ctx context.Context) (ClusterMetadata, error) {
	return ClusterMetadata{}, nil
}

func TestNodeGetInfoUsesMetadataService(t *testing.T) {
	server := &NodeServer{driver: &LinodeDriver{metadata: staticMetadataService{currentNode: NodeMetadata{KubernetesName: "worker-a"}}}}

	response, err := server.NodeGetInfo(context.Background(), &csi.NodeGetInfoRequest{})
	if err != nil {
		t.Fatalf("NodeGetInfo() error = %v", err)
	}
	if response.GetNodeId() != "worker-a" {
		t.Fatalf("unexpected NodeId %q", response.GetNodeId())
	}
}
