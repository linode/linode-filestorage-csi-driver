package driver

import (
	"context"
	"fmt"
	"sync"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"k8s.io/klog/v2"

	linodeclient "github.com/linode/linode-filestorage-csi-driver/pkg/linode-client"
	mountmanager "github.com/linode/linode-filestorage-csi-driver/pkg/mount-manager"
	"github.com/linode/linode-filestorage-csi-driver/pkg/util"
)

const Name = "linodenfs.csi.linode.com"

type Role string

const (
	RoleController Role = "controller"
	RoleNode       Role = "node"
)

type LinodeDriver struct {
	name          string
	vendorVersion string

	ids *IdentityServer
	cs  *ControllerServer
	ns  *NodeServer

	pluginCaps     []*csi.PluginCapability
	controllerCaps []*csi.ControllerServiceCapability
	nodeCaps       []*csi.NodeServiceCapability

	readyMu  sync.Mutex
	ready    bool
	metadata metadataService
	client   linodeclient.LinodeClient
}

func GetLinodeDriver(ctx context.Context) *LinodeDriver {
	klog.V(2).InfoS("creating LinodeDriver")
	return &LinodeDriver{
		controllerCaps: controllerServiceCapabilities(),
		nodeCaps:       nodeServiceCapabilities(),
	}
}

func (d *LinodeDriver) SetupLinodeDriver(
	ctx context.Context,
	client linodeclient.LinodeClient,
	mounter *mountmanager.SafeFormatAndMount,
	name string,
	vendorVersion string,
	role Role,
	nodeName string,
) error {
	if name == "" {
		return fmt.Errorf("driver name missing")
	}

	if role != RoleController && role != RoleNode {
		return errInvalidRole
	}

	d.name = name
	d.vendorVersion = vendorVersion
	d.pluginCaps = pluginCapabilities(role)
	klog.V(2).InfoS("configuring driver", "role", role, "name", name)
	metadataSvc, err := newMetadataService(ctx, nodeName, client)
	if err != nil {
		return fmt.Errorf("new metadata service: %w", err)
	}
	d.metadata = metadataSvc
	d.client = client

	ids, err := NewIdentityServer(ctx, d)
	if err != nil {
		return fmt.Errorf("new identity server: %w", err)
	}
	d.ids = ids

	switch role {
	case RoleController:
		cs, err := NewControllerServer(ctx, d, client, util.NewVolumeLocks())
		if err != nil {
			return fmt.Errorf("new controller server: %w", err)
		}
		d.cs = cs
	case RoleNode:
		ns, err := NewNodeServer(ctx, d, mounter, util.NewVolumeLocks())
		if err != nil {
			return fmt.Errorf("new node server: %w", err)
		}
		d.ns = ns
	}

	return nil
}

func (d *LinodeDriver) Run(ctx context.Context, endpoint string) {
	d.readyMu.Lock()
	d.ready = true
	d.readyMu.Unlock()

	server := NewNonBlockingGRPCServer()
	klog.V(2).InfoS("starting grpc server", "endpoint", endpoint)
	server.Start(endpoint, d.ids, d.cs, d.ns)
	server.Wait()
}
