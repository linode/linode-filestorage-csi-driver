package driver

import (
	"github.com/container-storage-interface/spec/lib/go/csi"
	"k8s.io/klog/v2"
)

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
