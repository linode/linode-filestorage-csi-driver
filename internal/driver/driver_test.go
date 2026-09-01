package driver

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/linode/linodego/v2"
	"go.uber.org/mock/gomock"
	"k8s.io/utils/mount"

	"github.com/linode/linode-filestorage-csi-driver/mocks"
	linodeclient "github.com/linode/linode-filestorage-csi-driver/pkg/linode-client"
	mountmanager "github.com/linode/linode-filestorage-csi-driver/pkg/mount-manager"
)

func withStubMetadataFactories(t *testing.T) {
	t.Helper()

	originalKubeFactory := newKubeNodeClient
	originalMetadataFactory := newInstanceMetadataClient
	ctrl := gomock.NewController(t)
	t.Cleanup(func() {
		newKubeNodeClient = originalKubeFactory
		newInstanceMetadataClient = originalMetadataFactory
	})

	newKubeNodeClient = func(context.Context) (KubeNodeClient, error) {
		return mocks.NewMockKubeNodeClient(ctrl), nil
	}
	newInstanceMetadataClient = func(context.Context) (InstanceMetadataClient, error) {
		return nil, errors.New("metadata unavailable")
	}
}

func defaultClientHelper(t *testing.T) *linodego.Client {
	t.Helper()

	config := &linodeclient.Config{
		LinodeToken:   "token",
		DriverVersion: "dev",
	}
	client, err := linodeclient.NewLinodeClient(config)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	return client
}

func defaultMounterHelper(t *testing.T) *mountmanager.SafeFormatAndMount {
	t.Helper()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	return &mountmanager.SafeFormatAndMount{
		SafeFormatAndMount: &mount.SafeFormatAndMount{
			Interface: mocks.NewMockMounter(ctrl),
			Exec:      mocks.NewMockExecutor(ctrl),
		},
	}
}

func TestSetupLinodeDriver(t *testing.T) {
	tests := []struct {
		name     string
		role     Role
		nodeName string
		wantErr  error
		assert   func(t *testing.T, driver *LinodeDriver)
	}{
		{
			name:   "assigns controller servers",
			role:   RoleController,
			assert: assertControllerDriverSetup,
		},
		{
			name:     "assigns node server",
			role:     RoleNode,
			nodeName: "node-a",
			assert:   assertNodeDriverSetup,
		},
		{
			name:    "rejects invalid role",
			role:    Role("all"),
			wantErr: errInvalidRole,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			withStubMetadataFactories(t)

			driver := GetLinodeDriver(ctx)
			client := defaultClientHelper(t)
			mounter := defaultMounterHelper(t)

			err := driver.SetupLinodeDriver(ctx, client, mounter, Name, "dev", tt.role, tt.nodeName)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("SetupLinodeDriver() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if tt.assert != nil {
				tt.assert(t, driver)
			}
		})
	}
}

func assertControllerDriverSetup(t *testing.T, driver *LinodeDriver) {
	t.Helper()

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
		csi.ControllerServiceCapability_RPC_GET_VOLUME,
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
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

func assertNodeDriverSetup(t *testing.T, driver *LinodeDriver) {
	t.Helper()

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
