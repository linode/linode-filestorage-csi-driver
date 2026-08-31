package driver

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	metadataapi "github.com/linode/go-metadata"
	"github.com/linode/linodego/v2"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/linode/linode-filestorage-csi-driver/mocks"
)

func TestMetadataServiceCurrentNode(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*mocks.MockInstanceMetadataClient, *mocks.MockKubeNodeClient)
		want  NodeMetadata
	}{
		{
			name: "uses instance metadata",
			setup: func(instanceClient *mocks.MockInstanceMetadataClient, kubeClient *mocks.MockKubeNodeClient) {
				instanceClient.EXPECT().GetInstance(gomock.Any()).Return(&metadataapi.InstanceData{ID: 101, Region: "us-east"}, nil)
				instanceClient.EXPECT().GetNetwork(gomock.Any()).Return(&metadataapi.NetworkData{
					IPv4: metadataapi.IPv4Data{Private: []netip.Prefix{netip.MustParsePrefix("10.0.0.12/24")}},
				}, nil)
			},
			want: NodeMetadata{KubernetesName: "worker-a", LinodeID: 101, Region: "us-east", AllowlistIP: "10.0.0.12"},
		},
		{
			name: "falls back to kubernetes",
			setup: func(instanceClient *mocks.MockInstanceMetadataClient, kubeClient *mocks.MockKubeNodeClient) {
				instanceClient.EXPECT().GetInstance(gomock.Any()).Return(nil, errors.New("metadata unavailable"))
				kubeClient.EXPECT().GetNode(gomock.Any(), "worker-a").Return(newTestNode("worker-a", "us-central", "10.0.0.20", "linode://202"), nil)
			},
			want: NodeMetadata{KubernetesName: "worker-a", LinodeID: 202, Region: "us-central", AllowlistIP: "10.0.0.20"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			instanceClient := mocks.NewMockInstanceMetadataClient(ctrl)
			kubeClient := mocks.NewMockKubeNodeClient(ctrl)
			if tt.setup != nil {
				tt.setup(instanceClient, kubeClient)
			}
			service := &metadataService{nodeName: "worker-a", instanceClient: instanceClient, kubeClient: kubeClient}

			node, err := service.CurrentNode(context.Background())
			if err != nil {
				t.Fatalf("CurrentNode() error = %v", err)
			}
			if node != tt.want {
				t.Fatalf("CurrentNode() = %#v, want %#v", node, tt.want)
			}
		})
	}
}

func TestMetadataServiceNodeByName(t *testing.T) {
	tests := []struct {
		name     string
		nodeName string
		setup    func(*mocks.MockKubeNodeClient)
		want     NodeMetadata
	}{
		{
			name:     "reads kubernetes node",
			nodeName: "worker-b",
			setup: func(kubeClient *mocks.MockKubeNodeClient) {
				kubeClient.EXPECT().GetNode(gomock.Any(), "worker-b").Return(newTestNode("worker-b", "eu-west", "10.1.2.3", "linode://303"), nil)
			},
			want: NodeMetadata{KubernetesName: "worker-b", LinodeID: 303, Region: "eu-west", AllowlistIP: "10.1.2.3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			kubeClient := mocks.NewMockKubeNodeClient(ctrl)
			if tt.setup != nil {
				tt.setup(kubeClient)
			}
			service := &metadataService{kubeClient: kubeClient}

			node, err := service.NodeByName(context.Background(), tt.nodeName)
			if err != nil {
				t.Fatalf("NodeByName() error = %v", err)
			}
			if node != tt.want {
				t.Fatalf("NodeByName() = %#v, want %#v", node, tt.want)
			}
		})
	}
}

func TestMetadataServiceCluster(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*mocks.MockKubeNodeClient, *mocks.MockLinodeClient)
		want    ClusterMetadata
		wantErr bool
	}{
		{
			name: "region and VPC via linode interfaces",
			setup: func(kubeClient *mocks.MockKubeNodeClient, linodeClient *mocks.MockLinodeClient) {
				kubeClient.EXPECT().ListNodes(gomock.Any()).Return(&corev1.NodeList{Items: []corev1.Node{
					*newTestNode("worker-a", "us-east", "10.0.0.10", "linode://11"),
					*newTestNode("worker-b", "us-east", "10.0.0.11", "linode://12"),
				}}, nil)
				linodeClient.EXPECT().GetInstance(gomock.Any(), 11).Return(&linodego.Instance{InterfaceGeneration: linodego.GenerationLinode}, nil)
				linodeClient.EXPECT().ListInterfaces(gomock.Any(), 11, gomock.Nil()).Return([]linodego.LinodeInterface{
					{VPC: &linodego.VPCInterface{VPCID: 123456}},
				}, nil)
			},
			want: ClusterMetadata{Region: "us-east", VPCID: 123456},
		},
		{
			name: "region and VPC via legacy config interfaces",
			setup: func(kubeClient *mocks.MockKubeNodeClient, linodeClient *mocks.MockLinodeClient) {
				kubeClient.EXPECT().ListNodes(gomock.Any()).Return(&corev1.NodeList{Items: []corev1.Node{
					*newTestNode("worker-a", "us-east", "10.0.0.10", "linode://11"),
				}}, nil)
				linodeClient.EXPECT().GetInstance(gomock.Any(), 11).Return(&linodego.Instance{InterfaceGeneration: linodego.GenerationLegacyConfig}, nil)
				linodeClient.EXPECT().ListInstanceConfigs(gomock.Any(), 11, gomock.Nil()).Return([]linodego.InstanceConfig{
					{Interfaces: []linodego.InstanceConfigInterface{
						{Purpose: linodego.InterfacePurposePublic, Active: true},
						{Purpose: linodego.InterfacePurposeVPC, Active: true, VPCID: new(654321)},
					}},
				}, nil)
			},
			want: ClusterMetadata{Region: "us-east", VPCID: 654321},
		},
		{
			name: "ignores inactive legacy config VPC interfaces",
			setup: func(kubeClient *mocks.MockKubeNodeClient, linodeClient *mocks.MockLinodeClient) {
				kubeClient.EXPECT().ListNodes(gomock.Any()).Return(&corev1.NodeList{Items: []corev1.Node{
					*newTestNode("worker-a", "us-east", "10.0.0.10", "linode://11"),
				}}, nil)
				linodeClient.EXPECT().GetInstance(gomock.Any(), 11).Return(&linodego.Instance{InterfaceGeneration: linodego.GenerationLegacyConfig}, nil)
				linodeClient.EXPECT().ListInstanceConfigs(gomock.Any(), 11, gomock.Nil()).Return([]linodego.InstanceConfig{
					{Interfaces: []linodego.InstanceConfigInterface{
						{Purpose: linodego.InterfacePurposeVPC, Active: false, VPCID: new(654321)},
					}},
				}, nil)
			},
			wantErr: true,
		},
		{
			name: "rejects mixed regions",
			setup: func(kubeClient *mocks.MockKubeNodeClient, linodeClient *mocks.MockLinodeClient) {
				kubeClient.EXPECT().ListNodes(gomock.Any()).Return(&corev1.NodeList{Items: []corev1.Node{
					*newTestNode("worker-a", "us-east", "10.0.0.10", "linode://11"),
					*newTestNode("worker-b", "eu-west", "10.0.0.11", "linode://12"),
				}}, nil)
			},
			wantErr: true,
		},
		{
			name: "rejects node without VPC",
			setup: func(kubeClient *mocks.MockKubeNodeClient, linodeClient *mocks.MockLinodeClient) {
				kubeClient.EXPECT().ListNodes(gomock.Any()).Return(&corev1.NodeList{Items: []corev1.Node{
					*newTestNode("worker-a", "us-east", "10.0.0.10", "linode://11"),
				}}, nil)
				linodeClient.EXPECT().GetInstance(gomock.Any(), 11).Return(&linodego.Instance{InterfaceGeneration: linodego.GenerationLinode}, nil)
				linodeClient.EXPECT().ListInterfaces(gomock.Any(), 11, gomock.Nil()).Return([]linodego.LinodeInterface{{Public: &linodego.PublicInterface{}}}, nil)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			kubeClient := mocks.NewMockKubeNodeClient(ctrl)
			linodeClient := mocks.NewMockLinodeClient(ctrl)
			if tt.setup != nil {
				tt.setup(kubeClient, linodeClient)
			}
			service := &metadataService{kubeClient: kubeClient, linodeClient: linodeClient}

			cluster, err := service.Cluster(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("Cluster() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if cluster != tt.want {
				t.Fatalf("Cluster() = %#v, want %#v", cluster, tt.want)
			}
		})
	}
}
func TestMetadataServiceConfigured(t *testing.T) {
	tests := []struct {
		name           string
		role           Role
		withInstance   bool
		withKubernetes bool
		withLinode     bool
		want           bool
	}{
		{
			name:         "node role accepts instance metadata client",
			role:         RoleNode,
			withInstance: true,
			want:         true,
		},
		{
			name:           "node role accepts kube client fallback",
			role:           RoleNode,
			withKubernetes: true,
			want:           true,
		},
		{
			name: "node role rejects missing clients",
			role: RoleNode,
			want: false,
		},
		{
			name:           "controller role requires kube and linode clients",
			role:           RoleController,
			withKubernetes: true,
			withLinode:     true,
			want:           true,
		},
		{
			name:           "controller role rejects kube client without linode client",
			role:           RoleController,
			withKubernetes: true,
			want:           false,
		},
		{
			name:         "controller role ignores instance metadata client alone",
			role:         RoleController,
			withInstance: true,
			want:         false,
		},
		{
			name:           "invalid role is not configured",
			role:           Role("invalid"),
			withInstance:   true,
			withKubernetes: true,
			withLinode:     true,
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			service := metadataService{}
			if tt.withInstance {
				service.instanceClient = mocks.NewMockInstanceMetadataClient(ctrl)
			}
			if tt.withKubernetes {
				service.kubeClient = mocks.NewMockKubeNodeClient(ctrl)
			}
			if tt.withLinode {
				service.linodeClient = mocks.NewMockLinodeClient(ctrl)
			}

			if got := service.configured(tt.role); got != tt.want {
				t.Fatalf("configured(%q) = %v, want %v", tt.role, got, tt.want)
			}
		})
	}
}

func TestAllowlistIPFromNode(t *testing.T) {
	tests := []struct {
		name string
		node *corev1.Node
		want string
	}{
		{
			name: "prefers internal IP",
			node: &corev1.Node{
				Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
					{Type: corev1.NodeExternalIP, Address: "198.51.100.1"},
					{Type: corev1.NodeInternalIP, Address: "10.0.0.42"},
				}},
			},
			want: "10.0.0.42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip, err := allowlistIPFromNode(tt.node)
			if err != nil {
				t.Fatalf("allowlistIPFromNode() error = %v", err)
			}
			if ip != tt.want {
				t.Fatalf("allowlistIPFromNode() = %q, want %q", ip, tt.want)
			}
		})
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
