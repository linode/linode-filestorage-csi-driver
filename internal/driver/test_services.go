package driver

import (
	"context"
	"errors"

	"github.com/container-storage-interface/spec/lib/go/csi"

	linodeclient "github.com/linode/linode-filestorage-csi-driver/pkg/linode-client"
	mountmanager "github.com/linode/linode-filestorage-csi-driver/pkg/mount-manager"
	"github.com/linode/linode-filestorage-csi-driver/pkg/util"
)

var errMetadataProviderNotFound = errors.New("metadata provider not found")

// NewTestMetadataProvider constructs a test-only MetadataProvider for external
// test packages using caller-supplied lower-level clients.
func NewTestMetadataProvider(
	nodeName string,
	instanceClient InstanceMetadataClient,
	kubeClient KubeNodeClient,
	linodeClient linodeclient.LinodeClient,
) MetadataProvider {
	return newMetadataServiceWithClients(nodeName, instanceClient, kubeClient, linodeClient)
}

// NewTestServices constructs CSI services for the embedded sanity suite using
// supplied test dependencies.
func NewTestServices(
	ctx context.Context,
	client linodeclient.LinodeClient,
	metadata MetadataProvider,
	mounter *mountmanager.SafeFormatAndMount,
) (csi.IdentityServer, csi.ControllerServer, csi.NodeServer, error) {
	if client == nil {
		return nil, nil, nil, errLinodeClientNotFound
	}
	if metadata == nil {
		return nil, nil, nil, errMetadataProviderNotFound
	}
	if mounter == nil {
		return nil, nil, nil, errNilMounter
	}

	driver := GetLinodeDriver(ctx)
	driver.name = Name
	driver.vendorVersion = "test"
	driver.pluginCaps = pluginCapabilities(RoleController)
	driver.metadata = metadata
	driver.client = client
	driver.ready = true

	identity, err := NewIdentityServer(ctx, driver)
	if err != nil {
		return nil, nil, nil, err
	}
	controller, err := NewControllerServer(ctx, driver, client, util.NewVolumeLocks())
	if err != nil {
		return nil, nil, nil, err
	}
	node, err := NewNodeServer(ctx, driver, mounter, util.NewVolumeLocks())
	if err != nil {
		return nil, nil, nil, err
	}

	return identity, controller, node, nil
}
