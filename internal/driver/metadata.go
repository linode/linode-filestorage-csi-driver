package driver

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	metadataapi "github.com/linode/go-metadata"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const (
	regionTopologyLabel     = "topology.kubernetes.io/region"
	betaRegionTopologyLabel = "failure-domain.beta.kubernetes.io/region"
)

var (
	errMetadataNodeNameRequired = errors.New("metadata service requires node name")
	errMetadataRegionNotFound   = errors.New("node region label not found")
	errMetadataAllowlistIP      = errors.New("node allowlist IP not found")
	errMetadataClusterNodes     = errors.New("cluster has no nodes")
)

// NodeMetadata contains the node facts the CSI controller and node servers use
// for topology and filesystem allowlisting decisions.
type NodeMetadata struct {
	KubernetesName string
	LinodeID       int
	Region         string
	AllowlistIP    string
}

// ClusterMetadata contains cluster-scoped facts derived from Kubernetes node
// state.
type ClusterMetadata struct {
	Region string
}

// MetadataService provides a shared source of node and cluster metadata to both
// the controller and node servers.
type MetadataService interface {
	CurrentNode(ctx context.Context) (NodeMetadata, error)
	NodeByName(ctx context.Context, name string) (NodeMetadata, error)
	NodesByName(ctx context.Context, names []string) ([]NodeMetadata, error)
	Cluster(ctx context.Context) (ClusterMetadata, error)
}

type instanceMetadataClient interface {
	GetInstance(ctx context.Context) (*metadataapi.InstanceData, error)
	GetNetwork(ctx context.Context) (*metadataapi.NetworkData, error)
}

var newInstanceMetadataClient = func(ctx context.Context) (instanceMetadataClient, error) {
	return metadataapi.NewClient(ctx)
}

type kubeNodeClient interface {
	GetNode(ctx context.Context, name string) (*corev1.Node, error)
	ListNodes(ctx context.Context) (*corev1.NodeList, error)
}

type metadataKubeClient struct {
	client kubernetes.Interface
}

func (k *metadataKubeClient) GetNode(ctx context.Context, name string) (*corev1.Node, error) {
	return k.client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
}

func (k *metadataKubeClient) ListNodes(ctx context.Context) (*corev1.NodeList, error) {
	return k.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
}

var newKubeNodeClient = func(ctx context.Context) (kubeNodeClient, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("in-cluster config: %w", err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes client: %w", err)
	}

	return &metadataKubeClient{client: client}, nil
}

type metadataService struct {
	nodeName       string
	instanceClient instanceMetadataClient
	kubeClient     kubeNodeClient
}

func newMetadataService(ctx context.Context, nodeName string) (MetadataService, error) {
	kubeClient, err := newKubeNodeClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes metadata client: %w", err)
	}

	instanceClient, err := newInstanceMetadataClient(ctx)
	if err != nil {
		klog.V(4).InfoS("metadata service will fall back to kubernetes node lookups", "error", err)
		instanceClient = nil
	}

	return &metadataService{
		nodeName:       nodeName,
		instanceClient: instanceClient,
		kubeClient:     kubeClient,
	}, nil
}

func (s *metadataService) CurrentNode(ctx context.Context) (NodeMetadata, error) {
	if s.nodeName == "" {
		return NodeMetadata{}, errMetadataNodeNameRequired
	}

	if s.instanceClient != nil {
		node, err := s.currentNodeFromMetadata(ctx)
		if err == nil {
			return node, nil
		}

		klog.V(4).InfoS("falling back to kubernetes node metadata", "node", s.nodeName, "error", err)
	}

	return s.NodeByName(ctx, s.nodeName)
}

func (s *metadataService) NodeByName(ctx context.Context, name string) (NodeMetadata, error) {
	if name == "" {
		return NodeMetadata{}, errMetadataNodeNameRequired
	}

	node, err := s.kubeClient.GetNode(ctx, name)
	if err != nil {
		return NodeMetadata{}, fmt.Errorf("get kubernetes node %q: %w", name, err)
	}

	linodeID, err := linodeIDFromProviderID(node.Spec.ProviderID)
	if err != nil {
		return NodeMetadata{}, fmt.Errorf("node %q: %w", node.Name, err)
	}

	region, err := regionFromNode(node)
	if err != nil {
		return NodeMetadata{}, fmt.Errorf("node %q: %w", node.Name, err)
	}

	allowlistIP, err := allowlistIPFromNode(node)
	if err != nil {
		return NodeMetadata{}, fmt.Errorf("node %q: %w", node.Name, err)
	}

	return NodeMetadata{
		KubernetesName: node.Name,
		LinodeID:       linodeID,
		Region:         region,
		AllowlistIP:    allowlistIP,
	}, nil
}

func (s *metadataService) NodesByName(ctx context.Context, names []string) ([]NodeMetadata, error) {
	results := make([]NodeMetadata, 0, len(names))
	for _, name := range names {
		node, err := s.NodeByName(ctx, name)
		if err != nil {
			return nil, err
		}
		results = append(results, node)
	}
	return results, nil
}

func (s *metadataService) Cluster(ctx context.Context) (ClusterMetadata, error) {
	nodes, err := s.kubeClient.ListNodes(ctx)
	if err != nil {
		return ClusterMetadata{}, fmt.Errorf("list kubernetes nodes: %w", err)
	}
	if len(nodes.Items) == 0 {
		return ClusterMetadata{}, errMetadataClusterNodes
	}

	region := ""
	for i := range nodes.Items {
		nodeRegion, err := regionFromNode(&nodes.Items[i])
		if err != nil {
			return ClusterMetadata{}, fmt.Errorf("node %q: %w", nodes.Items[i].Name, err)
		}

		if region == "" {
			region = nodeRegion
			continue
		}

		if nodeRegion != region {
			return ClusterMetadata{}, fmt.Errorf("cluster has multiple regions: %q and %q", region, nodeRegion)
		}
	}

	return ClusterMetadata{Region: region}, nil
}

func (s *metadataService) currentNodeFromMetadata(ctx context.Context) (NodeMetadata, error) {
	instance, err := s.instanceClient.GetInstance(ctx)
	if err != nil {
		return NodeMetadata{}, fmt.Errorf("get instance metadata: %w", err)
	}
	if instance.ID == 0 {
		return NodeMetadata{}, errors.New("instance metadata did not include a Linode ID")
	}
	if instance.Region == "" {
		return NodeMetadata{}, errMetadataRegionNotFound
	}

	network, err := s.instanceClient.GetNetwork(ctx)
	if err != nil {
		return NodeMetadata{}, fmt.Errorf("get network metadata: %w", err)
	}

	allowlistIP, err := allowlistIPFromNetwork(network)
	if err != nil {
		return NodeMetadata{}, err
	}

	return NodeMetadata{
		KubernetesName: s.nodeName,
		LinodeID:       instance.ID,
		Region:         instance.Region,
		AllowlistIP:    allowlistIP,
	}, nil
}

func linodeIDFromProviderID(providerID string) (int, error) {
	if providerID == "" {
		return 0, errors.New("provider ID is not set")
	}
	if !strings.HasPrefix(providerID, "linode://") {
		return 0, fmt.Errorf("invalid provider ID format %q", providerID)
	}

	linodeID, err := strconv.Atoi(strings.TrimPrefix(providerID, "linode://"))
	if err != nil {
		return 0, fmt.Errorf("parse Linode ID from provider ID: %w", err)
	}
	return linodeID, nil
}

func regionFromNode(node *corev1.Node) (string, error) {
	if node == nil {
		return "", errors.New("node is nil")
	}

	if region := node.Labels[regionTopologyLabel]; region != "" {
		return region, nil
	}
	if region := node.Labels[betaRegionTopologyLabel]; region != "" {
		return region, nil
	}

	return "", errMetadataRegionNotFound
}

func allowlistIPFromNode(node *corev1.Node) (string, error) {
	if node == nil {
		return "", errors.New("node is nil")
	}

	for _, addressType := range []corev1.NodeAddressType{corev1.NodeInternalIP, corev1.NodeExternalIP} {
		for i := range node.Status.Addresses {
			address := &node.Status.Addresses[i]
			if address.Type == addressType && address.Address != "" {
				return address.Address, nil
			}
		}
	}

	return "", errMetadataAllowlistIP
}

func allowlistIPFromNetwork(network *metadataapi.NetworkData) (string, error) {
	if network == nil {
		return "", errMetadataAllowlistIP
	}

	for i := range network.IPv4.Private {
		prefix := network.IPv4.Private[i]
		if prefix.Addr().IsValid() {
			return prefix.Addr().String(), nil
		}
	}
	for i := range network.IPv4.Public {
		prefix := network.IPv4.Public[i]
		if prefix.Addr().IsValid() {
			return prefix.Addr().String(), nil
		}
	}
	if network.IPv6.SLAAC.Addr().IsValid() {
		return network.IPv6.SLAAC.Addr().String(), nil
	}
	for i := range network.IPv6.Ranges {
		prefix := network.IPv6.Ranges[i]
		if prefix.Addr().IsValid() {
			return prefix.Addr().String(), nil
		}
	}

	return "", errMetadataAllowlistIP
}
