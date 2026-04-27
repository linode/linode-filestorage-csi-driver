package driver

import (
	"context"
	"testing"
)

func TestIdentityServerGetPluginInfo(t *testing.T) {
	d := GetLinodeDriver(context.Background())
	d.name = Name
	d.vendorVersion = "dev"
	ids, err := NewIdentityServer(context.Background(), d)
	if err != nil {
		t.Fatalf("new identity server: %v", err)
	}

	resp, err := ids.GetPluginInfo(context.Background(), nil)
	if err != nil {
		t.Fatalf("get plugin info: %v", err)
	}
	if resp.GetName() != Name {
		t.Fatalf("unexpected driver name: %s", resp.GetName())
	}
}
