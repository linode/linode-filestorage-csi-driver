package driver

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/linode/linodego/v2"
	corev1 "k8s.io/api/core/v1"

	linodeclient "github.com/linode/linode-filestorage-csi-driver/pkg/linode-client"
)

type stubKubeNodeClient struct {
	getNodeFn   func(ctx context.Context, name string) (*corev1.Node, error)
	listNodesFn func(ctx context.Context) (*corev1.NodeList, error)
}

func (s *stubKubeNodeClient) GetNode(ctx context.Context, name string) (*corev1.Node, error) {
	if s.getNodeFn != nil {
		return s.getNodeFn(ctx, name)
	}
	return nil, errors.New("unexpected GetNode call")
}

func (s *stubKubeNodeClient) ListNodes(ctx context.Context) (*corev1.NodeList, error) {
	if s.listNodesFn != nil {
		return s.listNodesFn(ctx)
	}
	return &corev1.NodeList{}, nil
}

func withStubMetadataFactories(t *testing.T) {
	t.Helper()

	originalKubeFactory := newKubeNodeClient
	originalMetadataFactory := newInstanceMetadataClient
	t.Cleanup(func() {
		newKubeNodeClient = originalKubeFactory
		newInstanceMetadataClient = originalMetadataFactory
	})

	newKubeNodeClient = func(context.Context) (kubeNodeClient, error) {
		return &stubKubeNodeClient{}, nil
	}
	newInstanceMetadataClient = func(context.Context) (instanceMetadataClient, error) {
		return nil, errors.New("metadata unavailable")
	}
}

func defaultClientHelper(t *testing.T) *linodego.Client {
	t.Helper()

	config := &linodeclient.Config{
		LinodeToken: "token",
		BaseURL:     "https://api.linode.com",
	}
	client, err := linodeclient.NewLinodeClient(config)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	return client
}

func TestSetupLinodeDriverAssignsRoleServers(t *testing.T) {
	ctx := context.Background()
	withStubMetadataFactories(t)

	driver := GetLinodeDriver(ctx)
	client := defaultClientHelper(t)

	if err := driver.SetupLinodeDriver(ctx, client, Name, "dev", RoleController, ""); err != nil {
		t.Fatalf("setup driver: %v", err)
	}

	if driver.ids == nil {
		t.Fatal("expected identity server")
	}
	if driver.cs == nil {
		t.Fatal("expected controller server")
	}
	if driver.ns != nil {
		t.Fatal("expected node server to be nil for controller role")
	}

	wantPluginCaps := []*csi.PluginCapability{
		{
			Type: &csi.PluginCapability_Service_{
				Service: &csi.PluginCapability_Service{Type: csi.PluginCapability_Service_CONTROLLER_SERVICE},
			},
		},
	}
	if !reflect.DeepEqual(driver.pluginCaps, wantPluginCaps) {
		t.Fatalf("unexpected plugin caps: %#v", driver.pluginCaps)
	}

	wantControllerCaps := []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
		csi.ControllerServiceCapability_RPC_LIST_VOLUMES,
		csi.ControllerServiceCapability_RPC_GET_CAPACITY,
		csi.ControllerServiceCapability_RPC_GET_VOLUME,
	}
	if len(driver.controllerCaps) != len(wantControllerCaps) {
		t.Fatalf("unexpected controller cap count: %d", len(driver.controllerCaps))
	}
	for idx, want := range wantControllerCaps {
		if got := driver.controllerCaps[idx].GetRpc().GetType(); got != want {
			t.Fatalf("controller cap %d = %v, want %v", idx, got, want)
		}
	}
}

func TestSetupLinodeDriverAssignsNodeServer(t *testing.T) {
	ctx := context.Background()
	withStubMetadataFactories(t)

	driver := GetLinodeDriver(ctx)
	client := defaultClientHelper(t)

	if err := driver.SetupLinodeDriver(ctx, client, Name, "dev", RoleNode, "node-a"); err != nil {
		t.Fatalf("setup driver: %v", err)
	}

	if driver.ids == nil {
		t.Fatal("expected identity server")
	}
	if driver.cs != nil {
		t.Fatal("expected controller server to be nil for node role")
	}
	if driver.ns == nil {
		t.Fatal("expected node server")
	}
	if len(driver.pluginCaps) != 0 {
		t.Fatalf("expected no plugin caps for node role, got %#v", driver.pluginCaps)
	}
}

func TestSetupLinodeDriverRejectsInvalidRole(t *testing.T) {
	ctx := context.Background()
	withStubMetadataFactories(t)

	driver := GetLinodeDriver(ctx)
	client := defaultClientHelper(t)

	err := driver.SetupLinodeDriver(ctx, client, Name, "dev", Role("all"), "")
	if !errors.Is(err, errInvalidRole) {
		t.Fatalf("expected errInvalidRole, got %v", err)
	}
}
