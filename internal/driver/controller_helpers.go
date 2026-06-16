package driver

import (
	"fmt"
	"net/http"
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/linode/linodego/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	storageClassParamSpaceID    = "space-id"
	storageClassParamSpaceLabel = "space-label"
	storageClassParamRootSquash = "filesystem-root-squash"
	storageClassParamTags       = Name + "/filesystemTags"

	volumeContextSpaceID       = "space-id"
	volumeContextFilesystemID  = "filesystem-id"
	volumeContextMountTarget   = "mount-target"
	volumeContextRegion        = "region"
	volumeContextOwnedByDriver = Name + "/owned-by-driver"
	volumeContextOwnedValue    = "true"
)

type createVolumeParameters struct {
	region        string
	spaceID       string
	spaceLabel    string
	tags          []string
	rootSquash    linodego.NFSRootSquashMode
	rootSquashSet bool
}

type volumeHandle struct {
	spaceID      string
	filesystemID string
}

func parseCreateVolumeParameters(params map[string]string) (createVolumeParameters, error) {
	parsed := createVolumeParameters{
		spaceID:    strings.TrimSpace(params[storageClassParamSpaceID]),
		spaceLabel: strings.TrimSpace(params[storageClassParamSpaceLabel]),
		tags:       splitTags(params[storageClassParamTags]),
	}

	if parsed.spaceID == "" && parsed.spaceLabel == "" {
		return createVolumeParameters{}, status.Error(codes.InvalidArgument, "StorageClass must set either space-id or space-label for an existing NFS Storage Space")
	}
	if parsed.spaceID != "" && parsed.spaceLabel != "" {
		return createVolumeParameters{}, status.Error(codes.InvalidArgument, "StorageClass parameters space-id and space-label are mutually exclusive")
	}

	if value := strings.TrimSpace(params[storageClassParamRootSquash]); value != "" {
		mode := linodego.NFSRootSquashMode(value)
		switch mode {
		case linodego.NFSRootSquashModeNone, linodego.NFSRootSquashModeRootSquash, linodego.NFSRootSquashModeAllSquash:
			parsed.rootSquash = mode
			parsed.rootSquashSet = true
		default:
			return createVolumeParameters{}, status.Errorf(codes.InvalidArgument, "unsupported filesystem-root-squash value %q", value)
		}
	}

	return parsed, nil
}

func splitTags(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}

	parts := strings.Split(value, ",")
	tags := make([]string, 0, len(parts))
	for _, part := range parts {
		tag := strings.TrimSpace(part)
		if tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

func parseVolumeHandle(volumeID string) (volumeHandle, error) {
	parts := strings.Split(strings.TrimSpace(volumeID), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return volumeHandle{}, status.Errorf(codes.InvalidArgument, "volume id %q must have format {space_id}/{filesystem_id}", volumeID)
	}
	return volumeHandle{spaceID: parts[0], filesystemID: parts[1]}, nil
}

func volumeID(spaceID, filesystemID string) string {
	return fmt.Sprintf("%s/%s", spaceID, filesystemID)
}

func requestedCapacityBytes(capacityRange *csi.CapacityRange) int64 {
	if capacityRange == nil {
		return 0
	}
	if capacityRange.GetRequiredBytes() > 0 {
		return capacityRange.GetRequiredBytes()
	}
	return capacityRange.GetLimitBytes()
}

func validateCreateVolumeCapabilities(capabilities []*csi.VolumeCapability) error {
	if len(capabilities) == 0 {
		return errNoVolumeCapabilities
	}
	for _, capability := range capabilities {
		if supported, message := volumeCapabilitySupported(capability); !supported {
			return status.Error(codes.InvalidArgument, message)
		}
	}
	return nil
}

func volumeCapabilitySupported(capability *csi.VolumeCapability) (supported bool, message string) {
	if capability == nil {
		return false, "no volume capability set"
	}
	if capability.GetMount() == nil {
		return false, "only mount volume capabilities are supported"
	}

	switch capability.GetAccessMode().GetMode() {
	case csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
		csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY,
		csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER,
		csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_MULTI_WRITER:
		return true, ""
	case csi.VolumeCapability_AccessMode_UNKNOWN:
		return false, "volume access mode is required"
	default:
		return false, fmt.Sprintf("unsupported volume access mode %s", capability.GetAccessMode().GetMode().String())
	}
}

func volumeContext(filesystem *linodego.NFSFilesystem, ownedByDriver bool) map[string]string {
	context := map[string]string{
		volumeContextSpaceID:      filesystem.SpaceID,
		volumeContextFilesystemID: filesystem.ID,
		volumeContextMountTarget:  filesystem.MountTarget,
		volumeContextRegion:       filesystem.Region,
	}
	if ownedByDriver {
		context[volumeContextOwnedByDriver] = volumeContextOwnedValue
	}
	return context
}

func csiVolume(filesystem *linodego.NFSFilesystem, capacityBytes int64, ownedByDriver bool) *csi.Volume {
	return &csi.Volume{
		VolumeId:      volumeID(filesystem.SpaceID, filesystem.ID),
		CapacityBytes: capacityBytes,
		VolumeContext: volumeContext(filesystem, ownedByDriver),
	}
}

func linodeError(err error, message string) error {
	if err == nil {
		return nil
	}

	switch {
	case linodego.IsNotFound(err):
		return status.Errorf(codes.NotFound, "%s: %v", message, err)
	case linodego.ErrHasStatus(err, http.StatusBadRequest, http.StatusUnprocessableEntity):
		return status.Errorf(codes.InvalidArgument, "%s: %v", message, err)
	case linodego.ErrHasStatus(err, http.StatusUnauthorized, http.StatusForbidden):
		return status.Errorf(codes.PermissionDenied, "%s: %v", message, err)
	case linodego.ErrHasStatus(err, http.StatusConflict):
		return status.Errorf(codes.FailedPrecondition, "%s: %v", message, err)
	case linodego.ErrHasStatus(err, http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout):
		return status.Errorf(codes.Unavailable, "%s: %v", message, err)
	default:
		return status.Errorf(codes.Internal, "%s: %v", message, err)
	}
}

func listOptionsForExactFields(fields map[string]string) (*linodego.ListOptions, error) {
	filter := linodego.Filter{}
	for key, value := range fields {
		filter.AddField(linodego.Eq, key, value)
	}

	filterBytes, err := filter.MarshalJSON()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "build Linode API filter: %v", err)
	}
	return linodego.NewListOptions(0, string(filterBytes)), nil
}

func containsInt(values []int, value int) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}
	return false
}

func appendUniqueInt(values []int, value int) []int {
	if containsInt(values, value) {
		return values
	}
	result := append([]int(nil), values...)
	return append(result, value)
}

func removeInt(values []int, value int) []int {
	result := make([]int, 0, len(values))
	for _, existing := range values {
		if existing != value {
			result = append(result, existing)
		}
	}
	return result
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	result := append([]string(nil), values...)
	return append(result, value)
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for idx := range left {
		if left[idx] != right[idx] {
			return false
		}
	}
	return true
}

func boolPtr(value bool) *bool {
	return &value
}

func filesystemPolicyUpdate(policy *linodego.NFSFilesystemAccessPolicy, enabled bool, linodeIDs []int) linodego.NFSFilesystemAccessPolicyUpdateOptions {
	return linodego.NFSFilesystemAccessPolicyUpdateOptions{
		Label:         policy.Label,
		Enabled:       boolPtr(enabled),
		LinodeIDs:     linodeIDs,
		RootSquash:    policy.RootSquash,
		Protocols:     policy.Protocols,
		PosixOverride: policy.PosixOverride,
	}
}

func filesystemPolicyRootSquashUpdate(policy *linodego.NFSFilesystemAccessPolicy, rootSquash linodego.NFSRootSquashMode) linodego.NFSFilesystemAccessPolicyUpdateOptions {
	return linodego.NFSFilesystemAccessPolicyUpdateOptions{
		Label:         policy.Label,
		Enabled:       boolPtr(policy.Enabled),
		LinodeIDs:     policy.LinodeIDs,
		RootSquash:    rootSquash,
		Protocols:     policy.Protocols,
		PosixOverride: policy.PosixOverride,
	}
}
