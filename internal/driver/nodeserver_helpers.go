package driver

import (
	"os"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"k8s.io/klog/v2"
	"k8s.io/utils/mount"

	"github.com/linode/linode-filestorage-csi-driver/pkg/filesystem"
)

const (
	rwPermission = os.FileMode(0o755)
)

// ensureMountPoint checks if the target path is a mount point or not.
// If not, it creates a directory at the target path.
func (ns *NodeServer) ensureMountPoint(path string, fs filesystem.FileSystem) (bool, error) {
	// Check if the target path is a mount point.
	notMnt, err := ns.mounter.IsLikelyNotMountPoint(path)
	if err != nil {
		// Checking IsNotExist returns true. If true, it means we need to create directory at the target path.
		if fs.IsNotExist(err) {
			if err = fs.MkdirAll(path, rwPermission); err != nil {
				return true, errInternal("Failed to create directory (%q): %v", path, err)
			}
		} else {
			// If the error is unknown, return an error.
			return true, errInternal("Unknown error when checking mount point (%q): %v", path, err)
		}
	}

	return notMnt, nil
}

func (ns *NodeServer) nodePublishVolume(req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	stagingTargetPath := req.GetStagingTargetPath()
	targetPath := req.GetTargetPath()
	volumeID := req.GetVolumeId()

	// Set mount options
	options := []string{bindMountOption}
	if capMount := req.GetVolumeCapability().GetMount(); capMount != nil {
		options = append(options, capMount.GetMountFlags()...)
	}
	if req.GetReadonly() {
		options = append(options, "ro")
		klog.V(4).InfoS("Volume will be mounted as read-only", "volumeID", volumeID)
	}

	// Mount stagingTargetPath to targetPath
	klog.V(4).InfoS("Mounting volume", "volumeID", volumeID, "stagingTargetPath", stagingTargetPath, "targetPath", targetPath, "options", options)
	// Do we need to consider any sensitive mount options?
	if err := ns.mounter.Mount(stagingTargetPath, targetPath, "nfs", options); err != nil {
		klog.Errorf("Mount %q failed for volumeID %s, cleaning up", targetPath, volumeID)
		if unmntErr := mount.CleanupMountPoint(stagingTargetPath, ns.mounter, false /* extensiveMountPointCheck */); unmntErr != nil {
			klog.Errorf("Unmount %q failed on volumeID %s: %v", targetPath, volumeID, unmntErr.Error())
		}
		return nil, errInternal("NodePublishVolume could not mount %s at %s: %v", stagingTargetPath, targetPath, err)
	}

	return &csi.NodePublishVolumeResponse{}, nil
}
