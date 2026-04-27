package driver

import (
	"context"
	"fmt"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/linode/linodego"
	"k8s.io/klog/v2"
)

type ControllerServer struct {
	driver *LinodeDriver
	client *linodego.Client
	csi.UnimplementedControllerServer
}

func NewControllerServer(ctx context.Context, driver *LinodeDriver, client *linodego.Client) (*ControllerServer, error) {
	_ = ctx
	klog.V(4).InfoS("creating controller server")
	if driver == nil {
		return nil, errNilDriver
	}
	if client == nil {
		return nil, fmt.Errorf("linode client cannot be nil")
	}
	return &ControllerServer{driver: driver, client: client}, nil
}

func (s *ControllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	_ = ctx
	klog.V(4).InfoS("handling controller rpc", "method", "CreateVolume")

	// Future implementation will validate StorageClass parameters and create a managed NFS share.
	_ = req
	return nil, errNotImplemented
}

func (s *ControllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	_ = ctx
	klog.V(4).InfoS("handling controller rpc", "method", "DeleteVolume")

	// Future implementation will map the CSI volume ID back to a Linode-managed share and remove it.
	_ = req
	return nil, errNotImplemented
}

func (s *ControllerServer) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	_ = ctx
	klog.V(4).InfoS("handling controller rpc", "method", "ValidateVolumeCapabilities")

	// Future implementation will compare the requested access mode and mount flags with supported NFS semantics.
	_ = req
	return nil, errNotImplemented
}

func (s *ControllerServer) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	_ = ctx
	klog.V(4).InfoS("handling controller rpc", "method", "ControllerGetCapabilities")

	_ = req
	return &csi.ControllerGetCapabilitiesResponse{Capabilities: s.driver.controllerCaps}, nil
}

func (s *ControllerServer) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	_ = ctx
	klog.V(4).InfoS("handling controller rpc", "method", "ControllerExpandVolume")

	// Future implementation will translate requested capacity into a backend quota or share-size update.
	_ = req
	return nil, errNotImplemented
}

func (s *ControllerServer) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	_ = ctx
	klog.V(4).InfoS("handling controller rpc", "method", "GetCapacity")

	// Future implementation will surface backend capacity information once the API semantics are known.
	_ = req
	return nil, errNotImplemented
}

func (s *ControllerServer) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	_ = ctx
	klog.V(4).InfoS("handling controller rpc", "method", "CreateSnapshot")

	// Future implementation will create a backend-native snapshot when the managed file storage API supports it.
	_ = req
	return nil, errNotImplemented
}

func (s *ControllerServer) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	_ = ctx
	klog.V(4).InfoS("handling controller rpc", "method", "DeleteSnapshot")

	// Future implementation will delete a previously created backend snapshot.
	_ = req
	return nil, errNotImplemented
}

func (s *ControllerServer) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	_ = ctx
	klog.V(4).InfoS("handling controller rpc", "method", "ListSnapshots")

	// Future implementation will list backend snapshots or filter a single snapshot by ID.
	_ = req
	return nil, errNotImplemented
}
