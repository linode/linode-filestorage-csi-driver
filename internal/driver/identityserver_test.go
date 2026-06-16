package driver

import (
	"context"
	"reflect"
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNewIdentityServer(t *testing.T) {
	tests := []struct {
		name    string
		driver  *LinodeDriver
		wantErr bool
	}{
		{
			name:   "success",
			driver: &LinodeDriver{},
		},
		{
			name:    "nil driver",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, err := NewIdentityServer(context.Background(), tt.driver)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewIdentityServer() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if ids == nil {
				t.Fatal("expected identity server")
			}
		})
	}
}

func TestIdentityServerGetPluginInfo(t *testing.T) {
	tests := []struct {
		name              string
		driver            *LinodeDriver
		wantName          string
		wantVendorVersion string
		wantCode          codes.Code
	}{
		{
			name:              "returns plugin info",
			driver:            &LinodeDriver{name: Name, vendorVersion: "dev"},
			wantName:          Name,
			wantVendorVersion: "dev",
		},
		{
			name:     "requires name",
			driver:   &LinodeDriver{vendorVersion: "dev"},
			wantCode: codes.Unavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids := &IdentityServer{driver: tt.driver}

			resp, err := ids.GetPluginInfo(context.Background(), &csi.GetPluginInfoRequest{})
			if status.Code(err) != tt.wantCode {
				t.Fatalf("GetPluginInfo() code = %v, want %v", status.Code(err), tt.wantCode)
			}
			if tt.wantCode != codes.OK {
				return
			}
			if resp.GetName() != tt.wantName {
				t.Fatalf("GetPluginInfo() name = %q, want %q", resp.GetName(), tt.wantName)
			}
			if resp.GetVendorVersion() != tt.wantVendorVersion {
				t.Fatalf("GetPluginInfo() vendorVersion = %q, want %q", resp.GetVendorVersion(), tt.wantVendorVersion)
			}
		})
	}
}

func TestIdentityServerGetPluginCapabilities(t *testing.T) {
	tests := []struct {
		name     string
		role     Role
		wantCaps []*csi.PluginCapability
	}{
		{
			name: "controller role",
			role: RoleController,
			wantCaps: []*csi.PluginCapability{
				{
					Type: &csi.PluginCapability_Service_{
						Service: &csi.PluginCapability_Service{Type: csi.PluginCapability_Service_CONTROLLER_SERVICE},
					},
				},
			},
		},
		{
			name:     "node role",
			role:     RoleNode,
			wantCaps: []*csi.PluginCapability{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids := &IdentityServer{driver: &LinodeDriver{pluginCaps: pluginCapabilities(tt.role)}}

			resp, err := ids.GetPluginCapabilities(context.Background(), &csi.GetPluginCapabilitiesRequest{})
			if err != nil {
				t.Fatalf("GetPluginCapabilities() error = %v", err)
			}

			if !reflect.DeepEqual(resp.GetCapabilities(), tt.wantCaps) {
				t.Fatalf("GetPluginCapabilities() = %#v, want %#v", resp.GetCapabilities(), tt.wantCaps)
			}
		})
	}
}

func TestIdentityServerProbe(t *testing.T) {
	tests := []struct {
		name        string
		driverReady bool
	}{
		{name: "ready", driverReady: true},
		{name: "not ready", driverReady: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids := &IdentityServer{driver: &LinodeDriver{ready: tt.driverReady}}

			resp, err := ids.Probe(context.Background(), &csi.ProbeRequest{})
			if err != nil {
				t.Fatalf("Probe() error = %v", err)
			}
			if resp.GetReady().GetValue() != tt.driverReady {
				t.Fatalf("Probe() ready = %v, want %v", resp.GetReady().GetValue(), tt.driverReady)
			}
		})
	}
}
