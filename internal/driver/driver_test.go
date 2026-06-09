package driver

import (
	"context"
	"errors"
	"testing"

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

func TestSetupLinodeDriverAssignsRoleServers(t *testing.T) {
	ctx := context.Background()
	withStubMetadataFactories(t)

	driver := GetLinodeDriver(ctx)
	client, err := linodeclient.NewLinodeClient("token", "ua", "https://api.linode.com")
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

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
}

func TestSetupLinodeDriverAssignsNodeServer(t *testing.T) {
	ctx := context.Background()
	withStubMetadataFactories(t)

	driver := GetLinodeDriver(ctx)
	client, err := linodeclient.NewLinodeClient("", "ua", "https://api.linode.com")
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

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
}

func TestSetupLinodeDriverRejectsInvalidRole(t *testing.T) {
	ctx := context.Background()
	withStubMetadataFactories(t)

	driver := GetLinodeDriver(ctx)
	client, err := linodeclient.NewLinodeClient("token", "ua", "https://api.linode.com")
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	err = driver.SetupLinodeDriver(ctx, client, Name, "dev", Role("all"), "")
	if !errors.Is(err, errInvalidRole) {
		t.Fatalf("expected errInvalidRole, got %v", err)
	}
}
