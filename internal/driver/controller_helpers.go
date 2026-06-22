package driver

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/linode/linodego/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/utils/ptr"
)

const (
	storageClassParamSpaceID    = "space-id"
	storageClassParamSpaceLabel = "space-label"
	storageClassParamRootSquash = "filesystem-root-squash"
	storageClassParamTags       = "tags"

	volumeContextSpaceID      = "space-id"
	volumeContextFilesystemID = "filesystem-id"
	volumeContextMountTarget  = "mount-target"
	volumeContextRegion       = "region"
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
		return createVolumeParameters{}, status.Error(codes.InvalidArgument, "StorageClass must set either space-id or space-label for a pre-created NFS Storage Space")
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

func parseVolumeHandleAndNodeID(volumeID, nodeID string) (volumeHandle, int, error) {
	handle, err := parseVolumeHandle(volumeID)
	if err != nil {
		return volumeHandle{}, 0, err
	}
	linodeID, err := strconv.Atoi(nodeID)
	if err != nil {
		return volumeHandle{}, 0, status.Errorf(codes.InvalidArgument, "node id %q must be a Linode ID", nodeID)
	}
	return handle, linodeID, nil
}

func (s *ControllerServer) getFilesystemPolicyForVolumeAndNode(ctx context.Context, volumeID, nodeID string) (volumeHandle, int, *linodego.NFSFilesystemAccessPolicy, error) {
	handle, linodeID, err := parseVolumeHandleAndNodeID(volumeID, nodeID)
	if err != nil {
		return volumeHandle{}, 0, nil, err
	}
	policy, err := s.client.GetNFSFilesystemAccessPolicy(ctx, handle.spaceID, handle.filesystemID)
	if err != nil {
		return volumeHandle{}, 0, nil, err
	}
	return handle, linodeID, policy, nil
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

func volumeContext(filesystem *linodego.NFSFilesystem) map[string]string {
	values := map[string]string{
		volumeContextSpaceID:      filesystem.SpaceID,
		volumeContextFilesystemID: filesystem.ID,
		volumeContextMountTarget:  filesystem.MountTarget,
		volumeContextRegion:       filesystem.Region,
	}
	return values
}

func csiVolume(filesystem *linodego.NFSFilesystem, capacityBytes int64) *csi.Volume {
	return &csi.Volume{
		VolumeId:      volumeID(filesystem.SpaceID, filesystem.ID),
		CapacityBytes: capacityBytes,
		VolumeContext: volumeContext(filesystem),
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

func filesystemPolicyUpdate(policy *linodego.NFSFilesystemAccessPolicy, enabled bool, linodeIDs []int) linodego.NFSFilesystemAccessPolicyUpdateOptions {
	return linodego.NFSFilesystemAccessPolicyUpdateOptions{
		Label:         policy.Label,
		Enabled:       ptr.To(enabled),
		LinodeIDs:     linodeIDs,
		RootSquash:    policy.RootSquash,
		Protocols:     policy.Protocols,
		PosixOverride: policy.PosixOverride,
	}
}

func filesystemPolicyRootSquashUpdate(policy *linodego.NFSFilesystemAccessPolicy, rootSquash linodego.NFSRootSquashMode) linodego.NFSFilesystemAccessPolicyUpdateOptions {
	return linodego.NFSFilesystemAccessPolicyUpdateOptions{
		Label:         policy.Label,
		Enabled:       ptr.To(policy.Enabled),
		LinodeIDs:     policy.LinodeIDs,
		RootSquash:    rootSquash,
		Protocols:     policy.Protocols,
		PosixOverride: policy.PosixOverride,
	}
}

func (s *ControllerServer) resolveSpace(ctx context.Context, params *createVolumeParameters) (*linodego.NFSSpace, error) {
	if params.spaceID != "" {
		space, err := s.client.GetNFSSpace(ctx, params.spaceID)
		if err != nil {
			return nil, linodeError(err, "get NFS space")
		}
		return space, nil
	}

	options, err := listOptionsForExactFields(map[string]string{"label": params.spaceLabel})
	if err != nil {
		return nil, err
	}
	spaces, err := s.client.ListNFSSpaces(ctx, options)
	if err != nil {
		return nil, linodeError(err, "list NFS spaces")
	}
	switch len(spaces) {
	case 0:
		return nil, status.Errorf(codes.NotFound, "NFS space with label %q was not found", params.spaceLabel)
	case 1:
		return &spaces[0], nil
	default:
		return nil, status.Errorf(codes.FailedPrecondition, "multiple NFS spaces match label %q", params.spaceLabel)
	}
}

func (s *ControllerServer) findExistingFilesystem(ctx context.Context, spaceID, label, region string) (*linodego.NFSFilesystem, bool, error) {
	options, err := listOptionsForExactFields(map[string]string{"label": label, "region": region})
	if err != nil {
		return nil, false, err
	}
	filesystems, err := s.client.ListNFSFilesystems(ctx, spaceID, options)
	if err != nil {
		return nil, false, linodeError(err, "list NFS filesystems")
	}
	switch len(filesystems) {
	case 0:
		return nil, false, nil
	case 1:
		filesystem := &filesystems[0]
		if filesystem.Label != label || filesystem.Region != region || filesystem.SpaceID != spaceID || filesystem.MountTarget == "" {
			return nil, false, status.Errorf(codes.AlreadyExists, "NFS filesystem %q already exists with incompatible parameters", label)
		}
		return filesystem, true, nil
	default:
		return nil, false, status.Errorf(codes.FailedPrecondition, "multiple NFS filesystems match label %q in space %q", label, spaceID)
	}
}

func (s *ControllerServer) validateExistingFilesystem(ctx context.Context, filesystem *linodego.NFSFilesystem, params *createVolumeParameters) error {
	if !slices.Equal(filesystem.Tags, params.tags) {
		return status.Errorf(codes.AlreadyExists, "NFS filesystem %q already exists with incompatible tags", filesystem.Label)
	}
	if !params.rootSquashSet {
		return nil
	}

	policy, err := s.client.GetNFSFilesystemAccessPolicy(ctx, filesystem.SpaceID, filesystem.ID)
	if err != nil {
		return linodeError(err, "get NFS filesystem access policy")
	}
	if policy.RootSquash != params.rootSquash {
		return status.Errorf(codes.AlreadyExists, "NFS filesystem %q already exists with incompatible root squash policy", filesystem.Label)
	}
	return nil
}

func (s *ControllerServer) ensureSpaceVPC(ctx context.Context, spaceID, vpcID string) error {
	if vpcID == "" {
		return status.Error(codes.FailedPrecondition, "this driver requires VPC-backed IPv6 connectivity; cluster VPC not found")
	}

	policy, err := s.client.GetNFSSpaceAccessPolicy(ctx, spaceID)
	if err != nil {
		return linodeError(err, "get NFS space access policy")
	}
	if slices.Contains(policy.VPCIDs, vpcID) {
		return nil
	}
	policy.VPCIDs = append(policy.VPCIDs, vpcID)
	if _, err := s.client.UpdateNFSSpaceAccessPolicy(ctx, spaceID, linodego.NFSSpaceAccessPolicyUpdateOptions{
		Label:        policy.Label,
		Enabled:      ptr.To(policy.Enabled),
		VPCIDs:       policy.VPCIDs,
		AllowedCIDRs: policy.AllowedCIDRs,
		MTLSMode:     policy.MTLSMode,
	}); err != nil {
		return linodeError(err, "update NFS space access policy")
	}
	return nil
}

func (s *ControllerServer) setInitialRootSquash(ctx context.Context, spaceID, filesystemID string, rootSquash linodego.NFSRootSquashMode) error {
	policy, err := s.client.GetNFSFilesystemAccessPolicy(ctx, spaceID, filesystemID)
	if err != nil {
		return linodeError(err, "get NFS filesystem access policy")
	}
	if policy.RootSquash == rootSquash {
		return nil
	}
	if _, err := s.client.UpdateNFSFilesystemAccessPolicy(ctx, spaceID, filesystemID, filesystemPolicyRootSquashUpdate(policy, rootSquash)); err != nil {
		return linodeError(err, "update NFS filesystem root squash")
	}
	return nil
}
