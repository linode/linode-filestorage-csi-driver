package driver

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	metadataapi "github.com/linode/go-metadata"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
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

// NodeMetadata contains the facts resolved for the node plugin's identity.
type NodeMetadata struct {
	KubernetesName string
	LinodeID       int
	Region         string
	AllowlistIP    string
}

// MetadataProvider supplies node identity and the controller's workload region.
type MetadataProvider interface {
	CurrentNode(context.Context) (NodeMetadata, error)
	Region(context.Context) (string, error)
}

var _ MetadataProvider = (*metadataService)(nil)

type InstanceMetadataClient interface {
	GetInstance(ctx context.Context) (*metadataapi.InstanceData, error)
	GetNetwork(ctx context.Context) (*metadataapi.NetworkData, error)
}

var newInstanceMetadataClient = func(ctx context.Context) (InstanceMetadataClient, error) {
	return metadataapi.NewClient(ctx)
}

type KubeNodeClient interface {
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
	return k.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
}

var newKubeNodeClient = func(kubeconfigPath string) (KubeNodeClient, error) {
	var (
		config *rest.Config
		err    error
	)
	if kubeconfigPath != "" {
		// Direct loading prevents empty explicit files from selecting in-cluster credentials.
		loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath}
		kubeconfig, err := loadingRules.Load()
		if err != nil {
			return nil, fmt.Errorf("load kubeconfig: %w", err)
		}
		config, err = clientcmd.NewNonInteractiveClientConfig(*kubeconfig, "", &clientcmd.ConfigOverrides{}, loadingRules).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("load kubeconfig: %w", err)
		}
	} else {
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("in-cluster config: %w", err)
		}
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes client: %w", err)
	}

	return &metadataKubeClient{client: client}, nil
}

type metadataService struct {
	nodeName       string
	instanceClient InstanceMetadataClient
	kubeClient     KubeNodeClient

	regionMu sync.Mutex
	region   string
}

func newMetadataService(ctx context.Context, nodeName string, role Role, kubeconfigPath string) (*metadataService, error) {
	kubeClient, err := newKubeNodeClient(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes metadata client: %w", err)
	}

	var instanceClient InstanceMetadataClient
	if role == RoleNode {
		instanceClient, err = newInstanceMetadataClient(ctx)
		if err != nil {
			klog.V(4).InfoS("metadata service will fall back to kubernetes node lookups", "error", err)
			instanceClient = nil
		}
	}

	return newMetadataServiceWithClients(nodeName, instanceClient, kubeClient), nil
}

func newMetadataServiceWithClients(
	nodeName string,
	instanceClient InstanceMetadataClient,
	kubeClient KubeNodeClient,
) *metadataService {
	return &metadataService{
		nodeName:       nodeName,
		instanceClient: instanceClient,
		kubeClient:     kubeClient,
	}
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

// Region caches only successful discovery; workers may not exist when the controller starts.
func (s *metadataService) Region(ctx context.Context) (string, error) {
	s.regionMu.Lock()
	defer s.regionMu.Unlock()
	if s.region != "" {
		return s.region, nil
	}

	nodes, err := s.kubeClient.ListNodes(ctx)
	if err != nil {
		return "", fmt.Errorf("list kubernetes nodes: %w", err)
	}
	if len(nodes.Items) == 0 {
		return "", errMetadataClusterNodes
	}
	region, err := regionFromNode(&nodes.Items[0])
	if err != nil {
		return "", fmt.Errorf("node %q: %w", nodes.Items[0].Name, err)
	}
	s.region = region
	return region, nil
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
