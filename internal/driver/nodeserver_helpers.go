package driver

import (
	"os"
	"path/filepath"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"k8s.io/klog/v2"

	"github.com/linode/linode-filestorage-csi-driver/pkg/filesystem"
)

const (
	rwPermission                   = os.FileMode(0o755)
	ownerGroupReadWritePermissions = os.FileMode(0o660)
)

// validateNodePublishVolumeRequest validates the node publish volume request.
// It checks the volume ID, staging target path, target path, and volume capability in the provided request.
func validateNodePublishVolumeRequest(req *csi.NodePublishVolumeRequest) error {
	klog.V(4).InfoS("Entering validateNodePublishVolumeRequest", "volumeID", req.GetVolumeId(), "stagingTargetPath", req.GetStagingTargetPath(), "targetPath", req.GetTargetPath())

	if req.GetVolumeId() == "" {
		return errNoVolumeID
	}
	if req.GetStagingTargetPath() == "" {
		return errNoStagingTargetPath
	}
	if req.GetTargetPath() == "" {
		return errNoTargetPath
	}
	if req.GetVolumeCapability() == nil {
		return errNoVolumeCapability
	}

	klog.V(4).InfoS("Exiting validateNodePublishVolumeRequest")
	return nil
}

// validateNodeUnpublishVolumeRequest validates the node unpublish volume request.
// It checks the volume ID and target path in the provided request.
func validateNodeUnpublishVolumeRequest(req *csi.NodeUnpublishVolumeRequest) error {
	klog.V(4).InfoS("Entering validateNodeUnpublishVolumeRequest", "volumeID", req.GetVolumeId(), "targetPath", req.GetTargetPath())

	if req.GetVolumeId() == "" {
		return errNoVolumeID
	}
	if req.GetTargetPath() == "" {
		return errNoTargetPath
	}

	klog.V(4).InfoS("Exiting validateNodeUnpublishVolumeRequest")
	return nil
}

// ensureMountPoint checks if the staging target path is a mount point or not.
// If not, it creates a directory at the target path.
func (ns *NodeServer) ensureMountPoint(path string, nfs filesystem.FileSystem) (bool, error) {
	klog.V(4).InfoS("Entering ensureMountPoint", "path", path)

	// Check if the staging target path is a mount point.
	notMnt, err := ns.mounter.IsLikelyNotMountPoint(path)
	if err != nil {
		// Checking IsNotExist returns true. If true, it mean we need to create directory at the target path.
		if nfs.IsNotExist(err) {
			if err = nfs.MkdirAll(path, rwPermission); err != nil {
				return true, errInternal("Failed to create directory (%q): %v", path, err)
			}
		} else {
			// If the error is unknown, return an error.
			return true, errInternal("Unknown error when checking mount point (%q): %v", path, err)
		}
	}

	klog.V(4).InfoS("Exiting ensureMountPoint", "notMnt", notMnt)
	return notMnt, nil
}

// nodePublishVolumeNFS handles the NodePublishVolume call for NFS volumes.
//
// It takes a CSI NodePublishVolumeRequest, a list of mount options, and a file system interface.
// The CSI NodePublishVolumeRequest contains the volume ID, target path, and publish context.
// The publish context is expected to contain the device path of the volume to be published.
// The function creates the target directory, creates a file to bind mount the block device to,
// and mounts the volume using the provided mount options.
// It returns a CSI NodePublishVolumeResponse and an error if the operation fails.
func (s *NodeServer) nodePublishVolumeNFS(req *csi.NodePublishVolumeRequest, mountOptions []string, fs filesystem.FileSystem) (*csi.NodePublishVolumeResponse, error) {
	klog.V(4).InfoS("Entering nodePublishVolumeNFS", "volumeID", req.GetVolumeId(), "targetPath", req.GetTargetPath(), "mountOptions", mountOptions)

	targetPath := req.GetTargetPath()
	targetPathDir := filepath.Dir(targetPath)

	// Get the device path from the request
	devicePath := req.GetPublishContext()["devicePath"]
	if devicePath == "" {
		return nil, errInternal("devicePath cannot be found")
	}

	// Create directory at the directory level of given path
	klog.V(4).InfoS("Making targetPathDir", "targetPathDir", targetPathDir)
	if err := fs.MkdirAll(targetPathDir, rwPermission); err != nil {
		klog.Error(err, "mkdir failed", "targetPathDir", targetPathDir)
		return nil, errInternal("Failed to create directory %q: %v", targetPathDir, err)
	}

	// Make file to bind mount block device to file
	klog.V(4).InfoS("Making target block bind mount device file", "targetPath", targetPath)
	file, err := fs.OpenFile(targetPath, os.O_CREATE, ownerGroupReadWritePermissions)
	if err != nil {
		if removeErr := fs.Remove(targetPath); removeErr != nil {
			return nil, errInternal("Failed remove mount target %q: %v", targetPath, err)
		}
		return nil, errInternal("Failed to create file %s: %v", targetPath, err)
	}
	defer func() {
		err = file.Close()
	}()

	// Mount the volume
	klog.V(4).InfoS("Mounting volume", "devicePath", devicePath, "targetPath", targetPath, "mountOptions", mountOptions)
	if err := s.mounter.Mount(devicePath, targetPath, "nfs", mountOptions); err != nil {
		klog.Error(err, "Failed to mount volume", "devicePath", devicePath, "targetPath", targetPath)
		if removeErr := fs.Remove(targetPath); removeErr != nil {
			return nil, errInternal("Failed to mount %q at %q: %v. Additionally, failed to remove mount target: %v", devicePath, targetPath, err, removeErr)
		}
		return nil, errInternal("Failed to mount %q at %q: %v", devicePath, targetPath, err)
	}
	klog.V(4).InfoS("Successfully published NFS volume", "devicePath", devicePath, "targetPath", targetPath)

	klog.V(4).Info("Exiting nodePublishVolumeNFS")
	return &csi.NodePublishVolumeResponse{}, nil
}
