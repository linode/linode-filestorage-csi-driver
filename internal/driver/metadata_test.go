package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	metadataapi "github.com/linode/go-metadata"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

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

func TestMetadataServiceRegionRetriesDiscovery(t *testing.T) {
	kubeClient := mocks.NewMockKubeNodeClient(gomock.NewController(t))
	apiErr := errors.New("API unavailable")
	gomock.InOrder(
		kubeClient.EXPECT().ListNodes(gomock.Any()).Return(nil, apiErr),
		kubeClient.EXPECT().ListNodes(gomock.Any()).Return(&corev1.NodeList{}, nil),
		kubeClient.EXPECT().ListNodes(gomock.Any()).Return(&corev1.NodeList{Items: []corev1.Node{{}}}, nil),
		kubeClient.EXPECT().ListNodes(gomock.Any()).Return(&corev1.NodeList{Items: []corev1.Node{{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{betaRegionTopologyLabel: "us-east"}},
		}}}, nil),
	)
	service := &metadataService{kubeClient: kubeClient}
	for _, wantErr := range []error{apiErr, errMetadataClusterNodes, errMetadataRegionNotFound} {
		if region, err := service.Region(t.Context()); !errors.Is(err, wantErr) || region != "" {
			t.Fatalf("Region() = %q, %v; want empty region and %v", region, err, wantErr)
		}
	}
	if region, err := service.Region(t.Context()); err != nil || region != "us-east" {
		t.Fatalf("Region() after failures = %q, %v; want us-east", region, err)
	}
}

func TestMetadataServiceRegionConcurrentCache(t *testing.T) {
	kubeClient := mocks.NewMockKubeNodeClient(gomock.NewController(t))
	kubeClient.EXPECT().ListNodes(gomock.Any()).Return(&corev1.NodeList{Items: []corev1.Node{
		*newTestNode("worker-a", "us-east", "", ""),
	}}, nil).Times(1)
	service := &metadataService{kubeClient: kubeClient}
	start := make(chan struct{})
	results := make(chan error, 8)
	for range 8 {
		go func() {
			<-start
			region, err := service.Region(t.Context())
			if err == nil && region != "us-east" {
				err = fmt.Errorf("Region() = %q, want us-east", region)
			}
			results <- err
		}()
	}
	close(start)
	for range 8 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
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

func TestMetadataServiceRegionUsesExplicitKubeconfig(t *testing.T) {
	var requests atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) > 1 || r.Method != http.MethodGet || r.URL.Path != "/api/v1/nodes" || r.URL.Query().Get("limit") != "1" {
			http.Error(w, "unexpected node lookup", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(&corev1.NodeList{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NodeList"},
			Items: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{
				Name: "workload-node", Labels: map[string]string{regionTopologyLabel: "us-central"},
			}}},
		}); err != nil {
			t.Errorf("encode fake node list: %v", err)
		}
	}))
	defer apiServer.Close()
	client, err := newKubeNodeClient(writeKubeconfig(t, apiServer.URL))
	if err != nil {
		t.Fatalf("new Kubernetes client: %v", err)
	}
	service := &metadataService{kubeClient: client}
	for range 2 {
		if region, err := service.Region(t.Context()); err != nil || region != "us-central" {
			t.Fatalf("Region() = %q, %v; want us-central from workload API", region, err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("node lookups = %d, want 1", got)
	}
}

func TestNewKubeNodeClientConfigurationErrors(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	if _, err := newKubeNodeClient(""); err == nil || !strings.Contains(err.Error(), "in-cluster config") {
		t.Fatalf("empty kubeconfig error = %v, want in-cluster config error", err)
	}

	path := filepath.Join(t.TempDir(), "invalid-kubeconfig")
	if err := os.WriteFile(path, []byte("not: [valid"), 0o600); err != nil {
		t.Fatalf("write invalid kubeconfig: %v", err)
	}
	if _, err := newKubeNodeClient(path); err == nil || !strings.Contains(err.Error(), "load kubeconfig") {
		t.Fatalf("invalid explicit kubeconfig error = %v, want load kubeconfig error", err)
	}
}

func TestNewKubeNodeClientRejectsEmptyExplicitKubeconfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty-kubeconfig")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write empty kubeconfig: %v", err)
	}
	if _, err := newKubeNodeClient(path); !errors.Is(err, clientcmd.ErrEmptyConfig) {
		t.Fatalf("empty explicit kubeconfig error = %v, want empty config error", err)
	}
}

func writeKubeconfig(t *testing.T, serverURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	contents := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: fake
  cluster:
    server: %q
contexts:
- name: fake
  context:
    cluster: fake
    user: fake
current-context: fake
users:
- name: fake
  user: {}
`, serverURL)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}
