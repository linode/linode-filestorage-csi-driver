package driver

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	metadataapi "github.com/linode/go-metadata"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type stubInstanceMetadataClient struct {
	getInstanceFn func(ctx context.Context) (*metadataapi.InstanceData, error)
	getNetworkFn  func(ctx context.Context) (*metadataapi.NetworkData, error)
}

func (s *stubInstanceMetadataClient) GetInstance(ctx context.Context) (*metadataapi.InstanceData, error) {
	if s.getInstanceFn == nil {
		return nil, errors.New("unexpected GetInstance call")
	}
	return s.getInstanceFn(ctx)
}

func (s *stubInstanceMetadataClient) GetNetwork(ctx context.Context) (*metadataapi.NetworkData, error) {
	if s.getNetworkFn == nil {
		return nil, errors.New("unexpected GetNetwork call")
	}
	return s.getNetworkFn(ctx)
}

func TestMetadataServiceCurrentNodeUsesInstanceMetadata(t *testing.T) {
	service := &metadataService{
		nodeName: "worker-a",
		instanceClient: &stubInstanceMetadataClient{
			getInstanceFn: func(context.Context) (*metadataapi.InstanceData, error) {
				return &metadataapi.InstanceData{ID: 101, Region: "us-east"}, nil
			},
			getNetworkFn: func(context.Context) (*metadataapi.NetworkData, error) {
				return &metadataapi.NetworkData{
					IPv4: metadataapi.IPv4Data{Private: []netip.Prefix{netip.MustParsePrefix("10.0.0.12/24")}},
				}, nil
			},
		},
		kubeClient: &stubKubeNodeClient{},
	}

	node, err := service.CurrentNode(context.Background())
	if err != nil {
		t.Fatalf("CurrentNode() error = %v", err)
	}

	if node.KubernetesName != "worker-a" {
		t.Fatalf("unexpected KubernetesName %q", node.KubernetesName)
	}
	if node.LinodeID != 101 {
		t.Fatalf("unexpected LinodeID %d", node.LinodeID)
	}
	if node.Region != "us-east" {
		t.Fatalf("unexpected Region %q", node.Region)
	}
	if node.AllowlistIP != "10.0.0.12" {
		t.Fatalf("unexpected AllowlistIP %q", node.AllowlistIP)
	}
}

func TestMetadataServiceCurrentNodeFallsBackToKubernetes(t *testing.T) {
	service := &metadataService{
		nodeName: "worker-a",
		instanceClient: &stubInstanceMetadataClient{
			getInstanceFn: func(context.Context) (*metadataapi.InstanceData, error) {
				return nil, errors.New("metadata unavailable")
			},
			getNetworkFn: func(context.Context) (*metadataapi.NetworkData, error) {
				return nil, errors.New("unexpected GetNetwork call")
			},
		},
		kubeClient: &stubKubeNodeClient{
			getNodeFn: func(ctx context.Context, name string) (*corev1.Node, error) {
				return newTestNode(name, "us-central", "10.0.0.20", "linode://202"), nil
			},
		},
	}

	node, err := service.CurrentNode(context.Background())
	if err != nil {
		t.Fatalf("CurrentNode() error = %v", err)
	}
	if node.LinodeID != 202 {
		t.Fatalf("unexpected LinodeID %d", node.LinodeID)
	}
	if node.AllowlistIP != "10.0.0.20" {
		t.Fatalf("unexpected AllowlistIP %q", node.AllowlistIP)
	}
}

func TestMetadataServiceNodeByName(t *testing.T) {
	service := &metadataService{
		kubeClient: &stubKubeNodeClient{
			getNodeFn: func(ctx context.Context, name string) (*corev1.Node, error) {
				return newTestNode(name, "eu-west", "10.1.2.3", "linode://303"), nil
			},
		},
	}

	node, err := service.NodeByName(context.Background(), "worker-b")
	if err != nil {
		t.Fatalf("NodeByName() error = %v", err)
	}
	if node.KubernetesName != "worker-b" {
		t.Fatalf("unexpected KubernetesName %q", node.KubernetesName)
	}
	if node.Region != "eu-west" {
		t.Fatalf("unexpected Region %q", node.Region)
	}
	if node.AllowlistIP != "10.1.2.3" {
		t.Fatalf("unexpected AllowlistIP %q", node.AllowlistIP)
	}
}

func TestMetadataServiceClusterRegion(t *testing.T) {
	service := &metadataService{
		kubeClient: &stubKubeNodeClient{
			listNodesFn: func(ctx context.Context) (*corev1.NodeList, error) {
				return &corev1.NodeList{Items: []corev1.Node{
					*newTestNode("worker-a", "us-east", "10.0.0.10", "linode://11"),
					*newTestNode("worker-b", "us-east", "10.0.0.11", "linode://12"),
				}}, nil
			},
		},
	}

	cluster, err := service.Cluster(context.Background())
	if err != nil {
		t.Fatalf("Cluster() error = %v", err)
	}
	if cluster.Region != "us-east" {
		t.Fatalf("unexpected cluster region %q", cluster.Region)
	}
}

func TestMetadataServiceClusterRegionRejectsMixedRegions(t *testing.T) {
	service := &metadataService{
		kubeClient: &stubKubeNodeClient{
			listNodesFn: func(ctx context.Context) (*corev1.NodeList, error) {
				return &corev1.NodeList{Items: []corev1.Node{
					*newTestNode("worker-a", "us-east", "10.0.0.10", "linode://11"),
					*newTestNode("worker-b", "eu-west", "10.0.0.11", "linode://12"),
				}}, nil
			},
		},
	}

	_, err := service.Cluster(context.Background())
	if err == nil {
		t.Fatal("expected Cluster() error for mixed regions")
	}
}

func TestAllowlistIPFromNodePrefersInternalIP(t *testing.T) {
	node := &corev1.Node{
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeExternalIP, Address: "198.51.100.1"},
			{Type: corev1.NodeInternalIP, Address: "10.0.0.42"},
		}},
	}

	ip, err := allowlistIPFromNode(node)
	if err != nil {
		t.Fatalf("allowlistIPFromNode() error = %v", err)
	}
	if ip != "10.0.0.42" {
		t.Fatalf("unexpected AllowlistIP %q", ip)
	}
}

func newTestNode(name, region, internalIP, providerID string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{regionTopologyLabel: region},
		},
		Spec: corev1.NodeSpec{ProviderID: providerID},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeInternalIP, Address: internalIP},
		}},
	}
}
