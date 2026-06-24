package driver

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/sys/unix"
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

	stagingTargetPath := req.GetStagingTargetPath()
	volumeID := req.GetVolumeId()

	s.mux.Lock()
	defer s.mux.Unlock()

	// Validate req (NodeUnstageVolumeRequest)
	klog.V(4).InfoS("Validating request", "volumeID", volumeID, "stagingTargetPath", stagingTargetPath)
	if err := validateNodeUnstageVolumeRequest(req); err != nil {
		return nil, err
	}

	klog.V(4).InfoS("Unmounting staging target path", "volumeID", volumeID, "stagingTargetPath", stagingTargetPath)
	if err := mount.CleanupMountPoint(stagingTargetPath, s.mounter.Interface, true /* bind mount */); err != nil {
		return nil, errInternal("NodeUnstageVolume failed to unmount at path %s: %v", stagingTargetPath, err)
	}

	klog.V(2).InfoS("Successfully completed", "volumeID", volumeID)
	return &csi.NodeUnstageVolumeResponse{}, nil
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

	// Check if target path is a valid mount point
	targetPath := req.GetTargetPath()
	klog.V(4).InfoS("Ensuring target path is a valid mount point", "volumeID", volumeID, "targetPath", targetPath)
	notMnt, err := s.ensureMountPoint(targetPath, filesystem.NewFileSystem())
	if err != nil {
		return nil, err
	}
	if !notMnt {
		klog.V(4).InfoS("Target path is already a mount point", "volumeID", volumeID, "targetPath", targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	return s.nodePublishVolume(req)
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

// unixStatfs is used to mock the unix.Statfs function.
var unixStatfs = unix.Statfs

func (s *NodeServer) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	klog.V(4).InfoS("handling node rpc", "method", "NodeGetVolumeStats")

	if req.GetVolumeId() == "" {
		return nil, errNoVolumeID
	}

	if req.GetVolumePath() == "" {
		return nil, errNoVolumePath
	}

	var statfs unix.Statfs_t
	// See http://man7.org/linux/man-pages/man2/statfs.2.html for details.
	err := unixStatfs(req.GetVolumePath(), &statfs)
	switch {
	case errors.Is(err, unix.EIO):
		// EIO is returned when the filesystem is not mounted.
		return &csi.NodeGetVolumeStatsResponse{
			VolumeCondition: &csi.VolumeCondition{
				Abnormal: true,
				Message:  fmt.Sprintf("failed to get stats: %v", err.Error()),
			},
		}, nil
	case errors.Is(err, unix.ENOENT):
		// ENOENT is returned when the volume path does not exist.
		return nil, errNotFound("volume path not found: %v", err.Error())
	case err != nil:
		// Any other error is considered an internal error.
		return nil, errInternal("failed to get stats: %v", err.Error())
	}

	response := &csi.NodeGetVolumeStatsResponse{
		Usage: []*csi.VolumeUsage{
			{
				Available: int64(statfs.Bavail) * int64(statfs.Bsize),
				Total:     int64(statfs.Blocks) * int64(statfs.Bsize),
				Used:      int64(statfs.Blocks-statfs.Bfree) * int64(statfs.Bsize),
				Unit:      csi.VolumeUsage_BYTES,
			},
			{
				Available: int64(statfs.Ffree),
				Total:     int64(statfs.Files),
				Used:      int64(statfs.Files) - int64(statfs.Ffree),
				Unit:      csi.VolumeUsage_INODES,
			},
		},
		VolumeCondition: &csi.VolumeCondition{
			Abnormal: false,
			Message:  "healthy",
		},
	}

	klog.V(2).Info("Successfully retrieved volume stats", "volumeID", req.GetVolumeId(), "volumePath", req.GetVolumePath(), "response", response)
	return response, nil
}

func (s *NodeServer) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	klog.V(4).InfoS("handling node rpc", "method", "NodeExpandVolume")

	// Future implementation is expected to stay controller-only for quota-backed NFS expansion.
	_ = req
	return nil, errNotImplemented
}
