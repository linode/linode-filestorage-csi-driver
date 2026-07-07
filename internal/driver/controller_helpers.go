package driver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
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

	volumeContextSpaceID       = "space-id"
	volumeContextFilesystemID  = "filesystem-id"
	volumeContextMountTarget   = "mount-target"
	volumeContextRegion        = "region"
	volumeContextSpaceMTLSMode = "mtls-mode"
)

type createVolumeParameters struct {
	region          string
	spaceID         int
	spaceLabel      string
	tags            []string
	squashPolicy    linodego.NFSSquashPolicy
	squashPolicySet bool
}

type volumeHandle struct {
	spaceID      int
	filesystemID int
}

func parseCreateVolumeParameters(params map[string]string) (createVolumeParameters, error) {
	spaceID := strings.TrimSpace(params[storageClassParamSpaceID])
	parsed := createVolumeParameters{
		spaceLabel: strings.TrimSpace(params[storageClassParamSpaceLabel]),
		tags:       splitTags(params[storageClassParamTags]),
	}
	if spaceID != "" {
		id, err := strconv.Atoi(spaceID)
		if err != nil || id <= 0 {
			return createVolumeParameters{}, status.Errorf(codes.InvalidArgument, "StorageClass parameter space-id %q must be a positive integer", spaceID)
		}
		parsed.spaceID = id
	}

	if parsed.spaceID == 0 && parsed.spaceLabel == "" {
		return createVolumeParameters{}, status.Error(codes.InvalidArgument, "StorageClass must set either space-id or space-label for a pre-created NFS Storage Space")
	}
	if parsed.spaceID != 0 && parsed.spaceLabel != "" {
		return createVolumeParameters{}, status.Error(codes.InvalidArgument, "StorageClass parameters space-id and space-label are mutually exclusive")
	}

	if value := strings.TrimSpace(params[storageClassParamRootSquash]); value != "" {
		mode := linodego.NFSSquashPolicy(value)
		switch mode {
		case linodego.NFSSquashPolicyNone, linodego.NFSSquashPolicyRootSquash, linodego.NFSSquashPolicyAllSquash:
			parsed.squashPolicy = mode
			parsed.squashPolicySet = true
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
	spaceID, err := strconv.Atoi(parts[0])
	if err != nil {
		return volumeHandle{}, status.Errorf(codes.InvalidArgument, "volume id %q has invalid space id", volumeID)
	}
	filesystemID, err := strconv.Atoi(parts[1])
	if err != nil {
		return volumeHandle{}, status.Errorf(codes.InvalidArgument, "volume id %q has invalid filesystem id", volumeID)
	}
	return volumeHandle{spaceID: spaceID, filesystemID: filesystemID}, nil
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
	policy, err := s.client.GetNFSFilesystemAccessPolicy(ctx, strconv.Itoa(handle.spaceID), strconv.Itoa(handle.filesystemID))
	if err != nil {
		return volumeHandle{}, 0, nil, err
	}
	return handle, linodeID, policy, nil
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

func volumeContext(filesystem *linodego.NFSFilesystem, mtlsMode linodego.NFSMTLSMode) map[string]string {
	values := map[string]string{
		volumeContextSpaceID:      strconv.Itoa(filesystem.SpaceID),
		volumeContextFilesystemID: strconv.Itoa(filesystem.ID),
		volumeContextMountTarget:  filesystemMountTarget(filesystem),
		volumeContextRegion:       filesystem.Region,
	}
	if mtlsMode != "" {
		values[volumeContextSpaceMTLSMode] = string(mtlsMode)
	}
	return values
}

func csiVolume(filesystem *linodego.NFSFilesystem, capacityBytes int64, mtlsMode linodego.NFSMTLSMode) *csi.Volume {
	return &csi.Volume{
		VolumeId:      fmt.Sprintf("%d/%d", filesystem.SpaceID, filesystem.ID),
		CapacityBytes: capacityBytes,
		VolumeContext: volumeContext(filesystem, mtlsMode),
	}
}

func validateFilesystemMountTarget(filesystem *linodego.NFSFilesystem) error {
	if filesystemMountTarget(filesystem) == "" {
		return status.Errorf(codes.FailedPrecondition, "NFS filesystem %d does not have a mount target", filesystem.ID)
	}
	return nil
}

func csiControllerVolumeStatus(policy *linodego.NFSFilesystemAccessPolicy) *csi.ControllerGetVolumeResponse_VolumeStatus {
	if !policy.Enabled {
		return &csi.ControllerGetVolumeResponse_VolumeStatus{}
	}

	return &csi.ControllerGetVolumeResponse_VolumeStatus{PublishedNodeIds: publishedNodeIDs(filesystemPolicyLinodeIDs(policy))}
}

func filesystemMountTarget(filesystem *linodego.NFSFilesystem) string {
	if filesystem == nil || filesystem.MountTargetFQDN == nil {
		return ""
	}
	return *filesystem.MountTargetFQDN
}

func filesystemPolicyLinodeIDs(policy *linodego.NFSFilesystemAccessPolicy) []int {
	if policy == nil || len(policy.LinodeACL) == 0 {
		return nil
	}
	ids := make([]int, 0, len(policy.LinodeACL))
	for i := range policy.LinodeACL {
		linode := &policy.LinodeACL[i]
		if linode.ID != 0 {
			ids = append(ids, linode.ID)
		}
	}
	return ids
}

func publishedNodeIDs(linodeIDs []int) []string {
	if len(linodeIDs) == 0 {
		return nil
	}

	nodeIDs := make([]string, 0, len(linodeIDs))
	for _, linodeID := range linodeIDs {
		nodeIDs = append(nodeIDs, strconv.Itoa(linodeID))
	}
	return nodeIDs
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

func linodeWaitError(err error, message string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return status.Errorf(codes.Canceled, "%s: %v", message, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Errorf(codes.DeadlineExceeded, "%s: %v", message, err)
	}
	return linodeError(err, message)
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
	options := linodego.NFSFilesystemAccessPolicyUpdateOptions{
		Label:        ptr.To(policy.Label),
		Enabled:      ptr.To(enabled),
		LinodeIDs:    ptr.To(linodeIDs),
		SquashPolicy: ptr.To(policy.SquashPolicy),
	}
	if policy.Protocols != nil {
		options.Protocols = ptr.To(policy.Protocols)
	}
	return options
}

func filesystemPolicySquashPolicyUpdate(policy *linodego.NFSFilesystemAccessPolicy, squashPolicy linodego.NFSSquashPolicy) linodego.NFSFilesystemAccessPolicyUpdateOptions {
	options := filesystemPolicyUpdate(policy, policy.Enabled, filesystemPolicyLinodeIDs(policy))
	options.SquashPolicy = ptr.To(squashPolicy)
	return options
}

func (s *ControllerServer) resolveSpace(ctx context.Context, params *createVolumeParameters) (*linodego.NFSSpace, error) {
	if params.spaceID != 0 {
		space, err := s.client.GetNFSSpace(ctx, strconv.Itoa(params.spaceID))
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

func (s *ControllerServer) findExistingFilesystem(ctx context.Context, spaceID int, label, region string) (*linodego.NFSFilesystem, bool, error) {
	options, err := listOptionsForExactFields(map[string]string{"label": label, "region": region})
	if err != nil {
		return nil, false, err
	}
	filesystems, err := s.client.ListNFSFilesystems(ctx, strconv.Itoa(spaceID), options)
	if err != nil {
		return nil, false, linodeError(err, "list NFS filesystems")
	}
	switch len(filesystems) {
	case 0:
		return nil, false, nil
	case 1:
		filesystem := &filesystems[0]
		if filesystem.Label != label || filesystem.Region != region || filesystem.SpaceID != spaceID {
			return nil, false, status.Errorf(codes.AlreadyExists, "NFS filesystem %q already exists with incompatible parameters", label)
		}
		return filesystem, true, nil
	default:
		return nil, false, status.Errorf(codes.FailedPrecondition, "multiple NFS filesystems match label %q in space %d", label, spaceID)
	}
}

func (s *ControllerServer) validateExistingFilesystem(ctx context.Context, filesystem *linodego.NFSFilesystem, params *createVolumeParameters) error {
	if !slices.Equal(filesystem.Tags, params.tags) {
		return status.Errorf(codes.AlreadyExists, "NFS filesystem %q already exists with incompatible tags", filesystem.Label)
	}
	if !params.squashPolicySet {
		return nil
	}

	policy, err := s.client.GetNFSFilesystemAccessPolicy(ctx, strconv.Itoa(filesystem.SpaceID), strconv.Itoa(filesystem.ID))
	if err != nil {
		return linodeError(err, "get NFS filesystem access policy")
	}
	if policy.SquashPolicy != params.squashPolicy {
		return status.Errorf(codes.AlreadyExists, "NFS filesystem %q already exists with incompatible root squash policy", filesystem.Label)
	}
	return nil
}

func (s *ControllerServer) readyExistingFilesystem(ctx context.Context, filesystem *linodego.NFSFilesystem, params *createVolumeParameters) (*linodego.NFSFilesystem, error) {
	var err error
	if filesystem.Status != linodego.NFSFilesystemStatusActive {
		filesystem, err = s.client.WaitForNFSFilesystemStatus(ctx, strconv.Itoa(filesystem.SpaceID), strconv.Itoa(filesystem.ID), linodego.NFSFilesystemStatusActive)
		if err != nil {
			return nil, linodeWaitError(err, "wait for NFS filesystem active")
		}
	}
	if err := s.validateExistingFilesystem(ctx, filesystem, params); err != nil {
		return nil, err
	}
	if err := validateFilesystemMountTarget(filesystem); err != nil {
		return nil, err
	}
	return filesystem, nil
}

func (s *ControllerServer) getSpaceAccessPolicy(ctx context.Context, spaceID int) (*linodego.NFSSpaceAccessPolicy, error) {
	policy, err := s.client.GetNFSSpaceAccessPolicy(ctx, strconv.Itoa(spaceID))
	if err != nil {
		return nil, linodeError(err, "get NFS space access policy")
	}
	return policy, nil
}

func (s *ControllerServer) ensureSpaceVPC(ctx context.Context, spaceID, vpcID int, policy *linodego.NFSSpaceAccessPolicy) error {
	if vpcID == 0 {
		return status.Error(codes.FailedPrecondition, "this driver requires VPC-backed IPv6 connectivity; cluster VPC not found")
	}

	if policy == nil {
		var err error
		policy, err = s.getSpaceAccessPolicy(ctx, spaceID)
		if err != nil {
			return err
		}
	}
	if slices.ContainsFunc(policy.VPCACL, func(vpc linodego.NFSSpaceAccessPolicyVPC) bool {
		return vpc.ID == vpcID
	}) {
		return nil
	}

	vpcs := make([]linodego.NFSSpaceAccessPolicyVPCOptions, 0, len(policy.VPCACL)+1)
	for i := range policy.VPCACL {
		vpc := &policy.VPCACL[i]
		vpcs = append(vpcs, linodego.NFSSpaceAccessPolicyVPCOptions{
			ID:      vpc.ID,
			Subnets: spaceAccessPolicySubnetIDs(vpc.Subnets),
		})
	}
	vpcs = append(vpcs, linodego.NFSSpaceAccessPolicyVPCOptions{ID: vpcID})
	if _, err := s.client.UpdateNFSSpaceAccessPolicy(ctx, strconv.Itoa(spaceID), linodego.NFSSpaceAccessPolicyUpdateOptions{
		Label:    ptr.To(policy.Label),
		Enabled:  ptr.To(policy.Enabled),
		VPCs:     ptr.To(vpcs),
		MTLSMode: ptr.To(policy.MTLSMode),
	}); err != nil {
		return linodeError(err, "update NFS space access policy")
	}
	if _, err := s.client.WaitForNFSSpaceAccessPolicyStatus(ctx, strconv.Itoa(spaceID), linodego.NFSAccessPolicyStatusActive); err != nil {
		return linodeWaitError(err, "wait for NFS space access policy active")
	}
	return nil
}

func spaceAccessPolicySubnetIDs(subnets []linodego.NFSSpaceAccessPolicyVPCSubnet) []int {
	if len(subnets) == 0 {
		return nil
	}
	ids := make([]int, 0, len(subnets))
	for i := range subnets {
		subnet := &subnets[i]
		if subnet.ID != 0 {
			ids = append(ids, subnet.ID)
		}
	}
	return ids
}

func (s *ControllerServer) setInitialSquashPolicy(ctx context.Context, spaceID, filesystemID int, squashPolicy linodego.NFSSquashPolicy) error {
	policy, err := s.client.GetNFSFilesystemAccessPolicy(ctx, strconv.Itoa(spaceID), strconv.Itoa(filesystemID))
	if err != nil {
		return linodeError(err, "get NFS filesystem access policy")
	}
	if policy.SquashPolicy == squashPolicy {
		return nil
	}
	if _, err := s.client.UpdateNFSFilesystemAccessPolicy(ctx, strconv.Itoa(spaceID), strconv.Itoa(filesystemID), filesystemPolicySquashPolicyUpdate(policy, squashPolicy)); err != nil {
		return linodeError(err, "update NFS filesystem squash policy")
	}
	if _, err := s.client.WaitForNFSFilesystemAccessPolicyStatus(ctx, strconv.Itoa(spaceID), strconv.Itoa(filesystemID), linodego.NFSAccessPolicyStatusActive); err != nil {
		return linodeWaitError(err, "wait for NFS filesystem access policy active")
	}
	return nil
}
