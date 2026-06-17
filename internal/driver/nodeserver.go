package driver

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"

	mountmanager "github.com/linode/linode-filestorage-csi-driver/pkg/mount-manager"
)

type NodeServer struct {
	driver  *LinodeDriver
	mounter *mountmanager.SafeFormatAndMount
	csi.UnimplementedNodeServer
}

func NewNodeServer(ctx context.Context, driver *LinodeDriver, mounter *mountmanager.SafeFormatAndMount) (*NodeServer, error) {
	klog.V(4).InfoS("creating node server")
	if driver == nil {
		return nil, errNilDriver
	}
	if mounter == nil {
		return nil, errNilMounter
	}
	return &NodeServer{driver: driver, mounter: mounter}, nil
}

func (s *NodeServer) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	klog.V(4).InfoS("handling node rpc", "method", "NodeGetInfo")

	_ = req
	if s.driver.metadata == nil {
		return nil, status.Error(codes.Internal, "metadata service is not configured")
	}

	node, err := s.driver.metadata.CurrentNode(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve node metadata: %v", err)
	}

	return &csi.NodeGetInfoResponse{NodeId: node.KubernetesName}, nil
}

func (s *NodeServer) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	klog.V(4).InfoS("handling node rpc", "method", "NodeGetCapabilities")

	_ = req
	return &csi.NodeGetCapabilitiesResponse{Capabilities: s.driver.nodeCaps}, nil
}

func (s *NodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	klog.V(4).InfoS("handling node rpc", "method", "NodeStageVolume")

	// Future implementation will mount server:/exportPath to the kubelet staging target.
	_ = req
	return nil, errNotImplemented
}

func (s *NodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	klog.V(4).InfoS("handling node rpc", "method", "NodeUnstageVolume")

	// Future implementation will unmount the staged NFS path and clean up node-local state.
	_ = req
	return nil, errNotImplemented
}

func (s *NodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	klog.V(4).InfoS("handling node rpc", "method", "NodePublishVolume")

	// Future implementation will bind-mount the staged path into the pod target path.
	_ = req
	return nil, errNotImplemented
}

func (s *NodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	klog.V(4).InfoS("handling node rpc", "method", "NodeUnpublishVolume")

	// Future implementation will unmount the published target path from the node.
	_ = req
	return nil, errNotImplemented
}

func (s *NodeServer) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	klog.V(4).InfoS("handling node rpc", "method", "NodeGetVolumeStats")

	// Future implementation will report filesystem usage for the mounted NFS path.
	_ = req
	return nil, errNotImplemented
}

func (s *NodeServer) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	klog.V(4).InfoS("handling node rpc", "method", "NodeExpandVolume")

	// Future implementation is expected to stay controller-only for quota-backed NFS expansion.
	_ = req
	return nil, errNotImplemented
}
