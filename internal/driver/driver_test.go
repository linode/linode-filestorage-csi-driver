package driver

import (
	"context"
	"errors"
	"testing"

	linodeclient "github.com/linode/linode-filestorage-csi-driver/pkg/linode-client"
)

func TestSetupLinodeDriverAssignsRoleServers(t *testing.T) {
	ctx := context.Background()
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
