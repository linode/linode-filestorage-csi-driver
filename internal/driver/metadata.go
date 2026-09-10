package driver

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	metadataapi "github.com/linode/go-metadata"
	"github.com/linode/linodego/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	linodeclient "github.com/linode/linode-filestorage-csi-driver/pkg/linode-client"
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
	VPCID  int
}

// MetadataProvider supplies the cluster and node facts consumed by CSI services.
type MetadataProvider interface {
	CurrentNode(context.Context) (NodeMetadata, error)
	Cluster(context.Context) (ClusterMetadata, error)
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
	return k.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
}

var newKubeNodeClient = func(ctx context.Context) (KubeNodeClient, error) {
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
	instanceClient InstanceMetadataClient
	kubeClient     KubeNodeClient
	linodeClient   linodeclient.LinodeClient
}

func newMetadataService(ctx context.Context, nodeName string, linodeClient linodeclient.LinodeClient) (metadataService, error) {
	kubeClient, err := newKubeNodeClient(ctx)
	if err != nil {
		return metadataService{}, fmt.Errorf("create kubernetes metadata client: %w", err)
	}

	instanceClient, err := newInstanceMetadataClient(ctx)
	if err != nil {
		klog.V(4).InfoS("metadata service will fall back to kubernetes node lookups", "error", err)
		instanceClient = nil
	}

	return newMetadataServiceWithClients(nodeName, instanceClient, kubeClient, linodeClient), nil
}

func newMetadataServiceWithClients(
	nodeName string,
	instanceClient InstanceMetadataClient,
	kubeClient KubeNodeClient,
	linodeClient linodeclient.LinodeClient,
) metadataService {
	return metadataService{
		nodeName:       nodeName,
		instanceClient: instanceClient,
		kubeClient:     kubeClient,
		linodeClient:   linodeClient,
	}
}

func (s metadataService) CurrentNode(ctx context.Context) (NodeMetadata, error) {
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

func (s metadataService) configured(role Role) bool {
	switch role {
	case RoleController:
		return s.kubeClient != nil && s.linodeClient != nil
	case RoleNode:
		return s.instanceClient != nil || s.kubeClient != nil
	default:
		return false
	}
}

func (s metadataService) NodeByName(ctx context.Context, name string) (NodeMetadata, error) {
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

func (s metadataService) NodesByName(ctx context.Context, names []string) ([]NodeMetadata, error) {
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

func (s metadataService) Cluster(ctx context.Context) (ClusterMetadata, error) {
	if s.linodeClient == nil {
		return ClusterMetadata{}, errLinodeClientNotFound
	}

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

	node := &nodes.Items[0]
	linodeID, err := linodeIDFromProviderID(node.Spec.ProviderID)
	if err != nil {
		return ClusterMetadata{}, fmt.Errorf("node %q: %w", node.Name, err)
	}

	vpcID, err := s.nodeVPCID(ctx, linodeID)
	if err != nil {
		return ClusterMetadata{}, fmt.Errorf("node %q: %w", node.Name, err)
	}

	return ClusterMetadata{Region: region, VPCID: vpcID}, nil
}

// nodeVPCID resolves the VPC a Linode is attached to. Linodes provisioned with
// the newer "linode" interface generation expose VPC attachment via
// ListInterfaces, but most existing Linodes (including LKE/CAPI-provisioned
// nodes) still use the legacy config-profile interfaces, where VPC attachment
// is a purpose="vpc" entry on the active InstanceConfig instead.
func (s metadataService) nodeVPCID(ctx context.Context, linodeID int) (int, error) {
	instance, err := s.linodeClient.GetInstance(ctx, linodeID)
	if err != nil {
		return 0, fmt.Errorf("get Linode instance %d: %w", linodeID, err)
	}

	if instance.InterfaceGeneration == linodego.GenerationLegacyConfig {
		return s.nodeVPCIDFromConfigs(ctx, linodeID)
	}
	return s.nodeVPCIDFromInterfaces(ctx, linodeID)
}

func (s metadataService) nodeVPCIDFromInterfaces(ctx context.Context, linodeID int) (int, error) {
	interfaces, err := s.linodeClient.ListInterfaces(ctx, linodeID, nil)
	if err != nil {
		return 0, fmt.Errorf("list Linode interfaces for %d: %w", linodeID, err)
	}

	vpcID := 0
	for i := range interfaces {
		iface := &interfaces[i]
		if iface.VPC == nil || iface.VPC.VPCID == 0 {
			continue
		}
		if err := mergeVPCID(&vpcID, iface.VPC.VPCID, linodeID); err != nil {
			return 0, err
		}
	}
	if vpcID == 0 {
		return 0, errClusterVPCNotFound
	}

	return vpcID, nil
}

func (s metadataService) nodeVPCIDFromConfigs(ctx context.Context, linodeID int) (int, error) {
	configs, err := s.linodeClient.ListInstanceConfigs(ctx, linodeID, nil)
	if err != nil {
		return 0, fmt.Errorf("list Linode instance configs for %d: %w", linodeID, err)
	}

	vpcID := 0
	for i := range configs {
		for j := range configs[i].Interfaces {
			iface := &configs[i].Interfaces[j]
			if !iface.Active || iface.Purpose != linodego.InterfacePurposeVPC || iface.VPCID == nil || *iface.VPCID == 0 {
				continue
			}
			if err := mergeVPCID(&vpcID, *iface.VPCID, linodeID); err != nil {
				return 0, err
			}
		}
	}
	if vpcID == 0 {
		return 0, errClusterVPCNotFound
	}

	return vpcID, nil
}

// mergeVPCID records candidate as the resolved VPC ID, or errors if a
// different VPC ID was already recorded for the same Linode.
func mergeVPCID(vpcID *int, candidate, linodeID int) error {
	if *vpcID == 0 {
		*vpcID = candidate
		return nil
	}
	if *vpcID != candidate {
		return fmt.Errorf("linode %d has multiple VPCs: %d and %d", linodeID, *vpcID, candidate)
	}
	return nil
}

func (s metadataService) currentNodeFromMetadata(ctx context.Context) (NodeMetadata, error) {
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
