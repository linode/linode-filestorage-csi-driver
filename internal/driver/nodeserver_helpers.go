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
	if err := ns.mounter.Mount(stagingTargetPath, targetPath, nfsFilesystemType, options); err != nil {
		klog.Errorf("Mount %q failed for volumeID %s, cleaning up", targetPath, volumeID)
		if unmntErr := mount.CleanupMountPoint(stagingTargetPath, ns.mounter, false /* extensiveMountPointCheck */); unmntErr != nil {
			klog.Errorf("Unmount %q failed on volumeID %s: %v", targetPath, volumeID, unmntErr.Error())
		}
		return nil, errInternal("NodePublishVolume could not mount %s at %s: %v", stagingTargetPath, targetPath, err)
	}

	return &csi.NodePublishVolumeResponse{}, nil
}

func (s *NodeServer) nodeStageVolume(req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	stagingTargetPath := req.GetStagingTargetPath()
	volumeID := req.GetVolumeId()

	klog.V(4).InfoS("Staging volume", "volumeID", volumeID, "stagingTargetPath", stagingTargetPath)

	source := req.GetVolumeContext()["mount-target"]
	mtlsMode := req.GetVolumeContext()["mtls-mode"]
	options := req.GetVolumeCapability().GetMount().GetMountFlags()
	switch mtlsMode {
	case "required":
		options = append(options, "xprtsec=mtls")
	case "optional":
		mtlsOptions := append(append([]string{}, options...), "xprtsec=mtls")
		// TODO(moshevayner): Once the NFS service is ready, add a check for the error type to determine if the error is due to mTLS being required and failing, or if it's a different error. This would allow us to differentiate between a failed mTLS mount and other mount errors.
		if err := s.mounter.Mount(source, stagingTargetPath, nfsFilesystemType, mtlsOptions); err == nil {
			klog.V(4).InfoS("Successfully staged with mTLS", "volumeID", volumeID)
			return &csi.NodeStageVolumeResponse{}, nil
		}
		klog.V(2).InfoS("Optional mTLS mount failed, retrying without mTLS", "volumeID", volumeID, "stagingTargetPath", stagingTargetPath)
	}

	if err := s.mounter.Mount(source, stagingTargetPath, nfsFilesystemType, options); err != nil {
		return nil, errInternal("NodeStageVolume failed to stage volume %s at path %s: %v", volumeID, stagingTargetPath, err)
	}

	klog.V(4).InfoS("Successfully staged", "volumeID", volumeID)
	return &csi.NodeStageVolumeResponse{}, nil
}

func allowedMTLSMode(mode string) bool {
	switch mode {
	case "required", "optional", "disabled":
		return true
	default:
		return false
	}
}
