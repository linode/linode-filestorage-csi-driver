package driver

import (
	"context"
	"strconv"
	"sync"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	"k8s.io/utils/mount"

	"github.com/linode/linode-filestorage-csi-driver/pkg/filesystem"
	mountmanager "github.com/linode/linode-filestorage-csi-driver/pkg/mount-manager"
)

type NodeServer struct {
	driver  *LinodeDriver
	mounter *mountmanager.SafeFormatAndMount
	mux     sync.Mutex

	csi.UnimplementedNodeServer
}

var _ csi.NodeServer = &NodeServer{}

const bindMountOption = "bind"

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
	if !s.driver.metadata.configured(RoleNode) {
		return nil, status.Error(codes.Internal, "metadata service is not configured")
	}

	node, err := s.driver.metadata.CurrentNode(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve node metadata: %v", err)
	}

	return &csi.NodeGetInfoResponse{NodeId: strconv.Itoa(node.LinodeID)}, nil
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

	volumeID := req.GetVolumeId()
	klog.V(2).InfoS("Processing request", "volumeID", volumeID)

	s.mux.Lock()
	defer s.mux.Unlock()

	// Validate the request object
	klog.V(4).InfoS("Validating request", "volumeID", volumeID)
	if err := validateNodePublishVolumeRequest(req); err != nil {
		return nil, err
	}

	// Set mount options
	options := []string{bindMountOption}
	options = append(options, req.GetVolumeCapability().GetMount().GetMountFlags()...)
	// TODO: add compatibility checks
	if req.GetReadonly() {
		options = append(options, "ro")
		klog.V(4).InfoS("Volume will be mounted as read-only", "volumeID", volumeID)
	}

	nfs := filesystem.NewFileSystem()
	// publish NFS volume
	if req.GetVolumeCapability().GetMount() != nil {
		klog.V(4).Info("Publishing volume as NFS volume", "volumeID", volumeID)
		response, err := s.nodePublishVolumeNFS(req, options, nfs)
		return response, err
	}

	targetPath := req.GetTargetPath()

	// Check if target path is a valid mount point
	klog.V(4).InfoS("Ensuring target path is a valid mount point", "volumeID", volumeID, "targetPath", targetPath)
	notMnt, err := s.ensureMountPoint(targetPath, nfs)
	if err != nil {
		return nil, err
	}
	if !notMnt {
		klog.V(4).InfoS("Target path is already a mount point", "volumeID", volumeID, "targetPath", targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	stagingTargetPath := req.GetStagingTargetPath()

	// Mount stagingTargetPath to targetPath
	klog.V(4).InfoS("Mounting volume", "volumeID", volumeID, "stagingTargetPath", stagingTargetPath, "targetPath", targetPath, "options", options)
	if err := s.mounter.Mount(stagingTargetPath, targetPath, "nfs", options); err != nil {
		return nil, errInternal("NodePublishVolume could not mount %s at %s: %v", stagingTargetPath, targetPath, err)
	}

	klog.V(4).InfoS("Successfully completed", "volumeID", volumeID)
	return &csi.NodePublishVolumeResponse{}, nil
}

func (s *NodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	klog.V(4).InfoS("handling node rpc", "method", "NodeUnpublishVolume")

	targetPath := req.GetTargetPath()
	volumeID := req.GetVolumeId()
	klog.V(2).InfoS("Processing request", "volumeID", volumeID, "targetPath", targetPath)

	s.mux.Lock()
	defer s.mux.Unlock()

	klog.V(4).InfoS("Validating request", "volumeID", volumeID, "targetPath", targetPath)

	if err := validateNodeUnpublishVolumeRequest(req); err != nil {
		return nil, err
	}

	// Unmount the target path and delete the remaining directory
	klog.V(4).InfoS("Unmounting and deleting target path", "volumeID", volumeID, "targetPath", targetPath)
	if err := mount.CleanupMountPoint(targetPath, s.mounter.Interface, true /* bind mount */); err != nil {
		return nil, errInternal("NodeUnpublishVolume could not unmount %s: %v", targetPath, err)
	}

	klog.V(2).InfoS("Successfully completed", "volumeID", volumeID, "targetPath", targetPath)
	return &csi.NodeUnpublishVolumeResponse{}, nil
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
