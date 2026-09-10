package driver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/linode/linodego/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	linodeclient "github.com/linode/linode-filestorage-csi-driver/pkg/linode-client"
	"github.com/linode/linode-filestorage-csi-driver/pkg/util"
)

const waitTimeout = 5 * time.Minute

const (
	// storageClassParamNamespace is the driver-qualified namespace for
	// StorageClass parameters.
	storageClassParamNamespace = Name + "/"

	storageClassParamSpaceID    = storageClassParamNamespace + "space-id"
	storageClassParamSpaceLabel = storageClassParamNamespace + "space-label"
	storageClassParamRootSquash = storageClassParamNamespace + "filesystem-root-squash"
	storageClassParamTags       = storageClassParamNamespace + "tags"

	volumeContextSpaceID       = "space-id"
	volumeContextFilesystemID  = "filesystem-id"
	volumeContextMountTarget   = "mount-target"
	volumeContextRegion        = "region"
	volumeContextSpaceMTLSMode = "mtls-mode"

	// Accepted values for the mtls-mode volume context key.
	mtlsModeRequired = "required"
	mtlsModeOptional = "optional"
	mtlsModeDisabled = "disabled"

	// Linode API filter fields used when listing NFS resources.
	filterFieldLabel  = "label"
	filterFieldRegion = "region"
)

// nfsLabelMaxBytes is the maximum length accepted by the NFS OpenAPI.
const nfsLabelMaxBytes = 63

// trailingHyphens matches one or more hyphens at the end of a string, used
// to strip a dangling hyphen run left behind by truncating a label.
var trailingHyphens = regexp.MustCompile(`-+$`)

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

type snapshotHandle struct {
	spaceID      int
	filesystemID int
	snapshotID   int
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
			return createVolumeParameters{}, status.Errorf(codes.InvalidArgument, "StorageClass parameter %s %q must be a positive integer", storageClassParamSpaceID, spaceID)
		}
		parsed.spaceID = id
	}

	if parsed.spaceID == 0 && parsed.spaceLabel == "" {
		return createVolumeParameters{}, status.Errorf(codes.InvalidArgument, "StorageClass must set either %s or %s for a pre-created NFS Storage Space", storageClassParamSpaceID, storageClassParamSpaceLabel)
	}
	if parsed.spaceID != 0 && parsed.spaceLabel != "" {
		return createVolumeParameters{}, status.Errorf(codes.InvalidArgument, "StorageClass parameters %s and %s are mutually exclusive", storageClassParamSpaceID, storageClassParamSpaceLabel)
	}

	if value := strings.TrimSpace(params[storageClassParamRootSquash]); value != "" {
		mode := linodego.NFSSquashPolicy(value)
		switch mode {
		case linodego.NFSSquashPolicyNone, linodego.NFSSquashPolicyRootSquash, linodego.NFSSquashPolicyAllSquash:
			parsed.squashPolicy = mode
			parsed.squashPolicySet = true
		default:
			return createVolumeParameters{}, status.Errorf(codes.InvalidArgument, "unsupported %s value %q", storageClassParamRootSquash, value)
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

func formatVolumeHandle(spaceID, filesystemID int) string {
	return fmt.Sprintf("%d/%d", spaceID, filesystemID)
}

func formatSnapshotHandle(spaceID, filesystemID, snapshotID int) string {
	return fmt.Sprintf("%d/%d/%d", spaceID, filesystemID, snapshotID)
}

func parseSnapshotHandle(snapshotID string) (snapshotHandle, error) {
	parts := strings.Split(strings.TrimSpace(snapshotID), "/")
	if len(parts) != 3 {
		return snapshotHandle{}, status.Errorf(codes.InvalidArgument, "snapshot id %q must have format {space_id}/{filesystem_id}/{snapshot_id}", snapshotID)
	}

	spaceID, spaceErr := strconv.Atoi(parts[0])
	filesystemID, filesystemErr := strconv.Atoi(parts[1])
	snapshot, snapshotErr := strconv.Atoi(parts[2])
	if spaceErr != nil || filesystemErr != nil || snapshotErr != nil {
		return snapshotHandle{}, status.Errorf(codes.InvalidArgument, "snapshot id %q must contain integer IDs", snapshotID)
	}

	return snapshotHandle{spaceID: spaceID, filesystemID: filesystemID, snapshotID: snapshot}, nil
}

func parseSnapshotContentSource(source *csi.VolumeContentSource) (*snapshotHandle, error) {
	if source == nil {
		return nil, nil //nolint:nilnil // A missing source represents regular provisioning.
	}
	if source.GetVolume() != nil || source.GetSnapshot() == nil {
		return nil, status.Error(codes.InvalidArgument, "unsupported volume content source")
	}

	snapshotID := source.GetSnapshot().GetSnapshotId()
	if snapshotID == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot id is required")
	}
	handle, err := parseSnapshotHandle(snapshotID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "snapshot id %q was not found", snapshotID)
	}
	return &handle, nil
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

func requestedCapacityBytes(capacityRange *csi.CapacityRange) int64 {
	if capacityRange == nil {
		return 0
	}
	if capacityRange.GetRequiredBytes() > 0 {
		return capacityRange.GetRequiredBytes()
	}
	return capacityRange.GetLimitBytes()
}

func validateExistingFilesystemCapacity(filesystem *linodego.NFSFilesystem, capacityRange *csi.CapacityRange) error {
	if filesystem.Stats.MaxCapacityBytes == nil {
		return nil
	}

	capacityBytes := *filesystem.Stats.MaxCapacityBytes
	if capacityRange.GetRequiredBytes() > capacityBytes || (capacityRange.GetLimitBytes() != 0 && capacityBytes > capacityRange.GetLimitBytes()) {
		return status.Errorf(codes.AlreadyExists, "NFS filesystem %q already exists with incompatible capacity %d", filesystem.Label, capacityBytes)
	}
	return nil
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
		VolumeId:      formatVolumeHandle(filesystem.SpaceID, filesystem.ID),
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

func waitContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, waitTimeout)
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
		Label:        new(policy.Label),
		Enabled:      new(enabled),
		LinodeIDs:    new(linodeIDs),
		SquashPolicy: new(policy.SquashPolicy),
	}
	if policy.Protocols != nil {
		options.Protocols = new(policy.Protocols)
	}
	return options
}

func filesystemPolicySquashPolicyUpdate(policy *linodego.NFSFilesystemAccessPolicy, squashPolicy linodego.NFSSquashPolicy) linodego.NFSFilesystemAccessPolicyUpdateOptions {
	options := filesystemPolicyUpdate(policy, policy.Enabled, filesystemPolicyLinodeIDs(policy))
	options.SquashPolicy = new(squashPolicy)
	return options
}

func (s *ControllerServer) resolveSpace(ctx context.Context, params *createVolumeParameters) (*linodego.NFSSpace, error) {
	if params.spaceID != 0 {
		space, err := s.client.GetNFSSpace(ctx, params.spaceID)
		if err != nil {
			return nil, linodeError(err, "get NFS space")
		}
		return space, nil
	}

	options, err := listOptionsForExactFields(map[string]string{filterFieldLabel: params.spaceLabel})
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

// normalizeLabel lowercases labels sent to the NFS backend while preserving all
// other characters unchanged.
func normalizeLabel(name string) string {
	return strings.ToLower(name)
}

// truncateNFSLabelToMaxBytes truncates a label to the NFS OpenAPI's maximum
// length, trimming any trailing hyphens left dangling by the cut.
func truncateNFSLabelToMaxBytes(label string) string {
	if len(label) <= nfsLabelMaxBytes {
		return label
	}

	return trailingHyphens.ReplaceAllString(label[:nfsLabelMaxBytes], "")
}

func (s *ControllerServer) findExistingFilesystem(ctx context.Context, spaceID int, label, region string) (*linodego.NFSFilesystem, bool, error) {
	options, err := listOptionsForExactFields(map[string]string{filterFieldLabel: label, filterFieldRegion: region})
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
		if filesystem.Label != label || filesystem.Region != region || filesystem.SpaceID != spaceID {
			return nil, false, status.Errorf(codes.AlreadyExists, "NFS filesystem %q already exists with incompatible parameters", label)
		}
		return filesystem, true, nil
	default:
		return nil, false, status.Errorf(codes.FailedPrecondition, "multiple NFS filesystems match label %q in space %d", label, spaceID)
	}
}

func validateExistingSnapshotClone(filesystem *linodego.NFSFilesystem, sourceSnapshotID int, region string) error {
	if filesystem.SourceSnapshotID == nil {
		return status.Errorf(codes.AlreadyExists, "NFS filesystem %d (%q) does not identify a source snapshot; requested snapshot %d", filesystem.ID, filesystem.Label, sourceSnapshotID)
	}
	if *filesystem.SourceSnapshotID != sourceSnapshotID {
		return status.Errorf(codes.AlreadyExists, "NFS filesystem %d (%q) was cloned from snapshot %d, requested snapshot %d", filesystem.ID, filesystem.Label, *filesystem.SourceSnapshotID, sourceSnapshotID)
	}
	if filesystem.Region != region {
		return status.Errorf(codes.AlreadyExists, "NFS filesystem %q already exists in incompatible region %q", filesystem.Label, filesystem.Region)
	}
	return nil
}

func (s *ControllerServer) validateExistingFilesystem(ctx context.Context, filesystem *linodego.NFSFilesystem, params *createVolumeParameters) error {
	if !slices.Equal(filesystem.Tags, params.tags) {
		return status.Errorf(codes.AlreadyExists, "NFS filesystem %q already exists with incompatible tags", filesystem.Label)
	}
	if !params.squashPolicySet {
		return nil
	}

	policy, err := s.client.GetNFSFilesystemAccessPolicy(ctx, filesystem.SpaceID, filesystem.ID)
	if err != nil {
		return linodeError(err, "get NFS filesystem access policy")
	}
	if policy.SquashPolicy != params.squashPolicy {
		return status.Errorf(codes.AlreadyExists, "NFS filesystem %q already exists with incompatible root squash policy", filesystem.Label)
	}
	return nil
}

func (s *ControllerServer) getSpaceAccessPolicy(ctx context.Context, spaceID int) (*linodego.NFSSpaceAccessPolicy, error) {
	policy, err := s.client.GetNFSSpaceAccessPolicy(ctx, spaceID)
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
	vpcAllowed := slices.ContainsFunc(policy.VPCACL, func(vpc linodego.NFSSpaceAccessPolicyVPC) bool {
		return vpc.ID == vpcID
	})
	if policy.Enabled && vpcAllowed {
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
	if !vpcAllowed {
		vpcs = append(vpcs, linodego.NFSSpaceAccessPolicyVPCOptions{ID: vpcID})
	}
	if _, err := s.client.UpdateNFSSpaceAccessPolicy(ctx, spaceID, linodego.NFSSpaceAccessPolicyUpdateOptions{
		Label:    new(policy.Label),
		Enabled:  new(true),
		VPCs:     new(vpcs),
		MTLSMode: new(policy.MTLSMode),
	}); err != nil {
		return linodeError(err, "update NFS space access policy")
	}
	waitCtx, cancel := waitContext(ctx)
	defer cancel()
	if _, err := s.client.WaitForNFSSpaceAccessPolicyStatus(waitCtx, spaceID, linodego.NFSAccessPolicyStatusActive); err != nil {
		return linodeWaitError(err, "wait for NFS space access policy active")
	}
	return nil
}

func (s *ControllerServer) handleExistingFilesystem(ctx context.Context, existing *linodego.NFSFilesystem, params *createVolumeParameters, space *linodego.NFSSpace, vpcID int, capacityRange *csi.CapacityRange, source *snapshotHandle) (*csi.CreateVolumeResponse, error) {
	if source != nil {
		if err := validateExistingSnapshotClone(existing, source.snapshotID, params.region); err != nil {
			return nil, err
		}
	}

	waitCtx, cancel := waitContext(ctx)
	defer cancel()
	existing, err := s.client.WaitForNFSFilesystemStatus(waitCtx, existing.SpaceID, existing.ID, linodego.NFSFilesystemStatusActive)
	if err != nil {
		return nil, linodeWaitError(err, "wait for NFS filesystem active")
	}
	if err := validateExistingFilesystemCapacity(existing, capacityRange); err != nil {
		return nil, err
	}
	if source != nil {
		if err := validateExistingSnapshotClone(existing, source.snapshotID, params.region); err != nil {
			return nil, err
		}
	}
	if err := s.validateExistingFilesystem(ctx, existing, params); err != nil {
		return nil, err
	}
	if err := validateFilesystemMountTarget(existing); err != nil {
		return nil, err
	}
	spacePolicy, err := s.getSpaceAccessPolicy(ctx, space.ID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureSpaceVPC(ctx, space.ID, vpcID, spacePolicy); err != nil {
		return nil, err
	}

	return &csi.CreateVolumeResponse{Volume: csiVolume(existing, requestedCapacityBytes(capacityRange), spacePolicy.MTLSMode)}, nil
}

func (s *ControllerServer) restoreFromSnapshot(ctx context.Context, label string, source snapshotHandle, spaceID int, params *createVolumeParameters, capacityBytes int64, spacePolicy *linodego.NFSSpaceAccessPolicy) (*csi.CreateVolumeResponse, error) {
	options := linodego.NFSSnapshotCloneOptions{
		Label:   label,
		Region:  params.region,
		SpaceID: new(new(spaceID)),
	}
	if params.tags != nil {
		options.Tags = new(params.tags)
	}
	cloned, err := s.client.CloneNFSSnapshot(ctx, source.spaceID, source.filesystemID, source.snapshotID, options)
	if err != nil {
		return nil, linodeError(err, "clone snapshot failed")
	}

	waitCloneCtx, cancel := waitContext(ctx)
	defer cancel()
	cloned, err = s.client.WaitForNFSFilesystemStatus(waitCloneCtx, cloned.SpaceID, cloned.ID, linodego.NFSFilesystemStatusActive)
	if err != nil {
		return nil, linodeWaitError(err, "wait for NFS cloned filesystem active")
	}
	if err := validateFilesystemMountTarget(cloned); err != nil {
		return nil, err
	}
	if params.squashPolicySet {
		if err := s.setInitialSquashPolicy(ctx, cloned.SpaceID, cloned.ID, params.squashPolicy); err != nil {
			return nil, err
		}
	}

	volume := csiVolume(cloned, capacityBytes, spacePolicy.MTLSMode)
	volume.ContentSource = &csi.VolumeContentSource{
		Type: &csi.VolumeContentSource_Snapshot{
			Snapshot: &csi.VolumeContentSource_SnapshotSource{
				SnapshotId: formatSnapshotHandle(source.spaceID, source.filesystemID, source.snapshotID),
			},
		},
	}
	return &csi.CreateVolumeResponse{Volume: volume}, nil
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
	policy, err := s.client.GetNFSFilesystemAccessPolicy(ctx, spaceID, filesystemID)
	if err != nil {
		return linodeError(err, "get NFS filesystem access policy")
	}
	if policy.SquashPolicy == squashPolicy {
		return nil
	}
	if _, err := s.client.UpdateNFSFilesystemAccessPolicy(ctx, spaceID, filesystemID, filesystemPolicySquashPolicyUpdate(policy, squashPolicy)); err != nil {
		return linodeError(err, "update NFS filesystem squash policy")
	}
	return s.waitForFilesystemAccessPolicyActive(ctx, spaceID, filesystemID)
}

func (s *ControllerServer) waitForFilesystemAccessPolicyActive(ctx context.Context, spaceID, filesystemID int) error {
	waitCtx, cancel := waitContext(ctx)
	defer cancel()
	if _, err := s.client.WaitForNFSFilesystemAccessPolicyStatus(waitCtx, spaceID, filesystemID, linodego.NFSAccessPolicyStatusActive); err != nil {
		return linodeWaitError(err, "wait for NFS filesystem access policy active")
	}
	return nil
}

func (s *ControllerServer) findSnapshotByLabel(ctx context.Context, handle volumeHandle, label string) (*linodego.NFSSnapshot, error) {
	snapshots, err := s.client.ListNFSSnapshots(ctx, handle.spaceID, handle.filesystemID, &linodego.ListOptions{
		PageOptions: &linodego.PageOptions{},
		PageSize:    linodeclient.DefaultListPageSize,
	})
	if err != nil {
		return nil, err
	}
	for i := range snapshots {
		if snapshots[i].Label == label {
			return &snapshots[i], nil
		}
	}
	return nil, nil //nolint:nilnil // Snapshot absence is the expected create path.
}

func (s *ControllerServer) listSnapshotByID(ctx context.Context, snapshotID, sourceVolumeID string) (*csi.ListSnapshotsResponse, error) {
	parsedSnapshot, err := parseSnapshotHandle(snapshotID)
	if err != nil {
		return nil, err
	}
	handle := volumeHandle{spaceID: parsedSnapshot.spaceID, filesystemID: parsedSnapshot.filesystemID}

	// A source volume filter that does not match this snapshot's volume (including
	// a malformed one) cannot describe it, so treat it as "no match" and return an
	// empty result.
	if sourceVolumeID != "" && sourceVolumeID != formatVolumeHandle(handle.spaceID, handle.filesystemID) {
		return &csi.ListSnapshotsResponse{}, nil
	}

	snapshot, err := s.client.GetNFSSnapshot(ctx, handle.spaceID, handle.filesystemID, parsedSnapshot.snapshotID)
	if err != nil {
		if linodego.IsNotFound(err) {
			return &csi.ListSnapshotsResponse{}, nil
		}
		return nil, linodeError(err, "get NFS snapshot")
	}

	entries, err := listSnapshotEntries([]linodego.NFSSnapshot{*snapshot}, handle)
	if err != nil {
		return nil, err
	}
	return &csi.ListSnapshotsResponse{Entries: entries}, nil
}

// listSnapshotsBySourceVolume fetches the full snapshot set once and slices it
// in memory. The CSI token is an absolute offset (not a linodego page number)
// so it can honor a max_entries that changes between calls, which a fixed page
// cursor cannot. Refetching the whole set is fine: snapshot sets are small.
//
// In production this path is unlikely to run: the external-snapshotter sidecar
// only ever calls ListSnapshots by snapshot_id, never by source volume.
func (s *ControllerServer) listSnapshotsBySourceVolume(ctx context.Context, sourceVolumeID, startingToken string, maxEntries int) (*csi.ListSnapshotsResponse, error) {
	offset, err := listSnapshotsStartingOffset(startingToken)
	if err != nil {
		return nil, err
	}

	handle, err := parseVolumeHandle(sourceVolumeID)
	if err != nil {
		return nil, err
	}

	snapshots, err := s.client.ListNFSSnapshots(ctx, handle.spaceID, handle.filesystemID, &linodego.ListOptions{
		PageOptions: &linodego.PageOptions{},
		PageSize:    linodeclient.DefaultListPageSize,
	})
	if err != nil {
		if !linodego.IsNotFound(err) {
			return nil, linodeError(err, "list NFS snapshots")
		}
		snapshots = nil
	}

	// Slice to the requested page before building CSI entries so we only parse
	// and allocate for the snapshots actually returned.
	page, nextToken, err := paginateSnapshots(snapshots, startingToken, offset, maxEntries)
	if err != nil {
		return nil, err
	}
	entries, err := listSnapshotEntries(page, handle)
	if err != nil {
		return nil, err
	}
	return &csi.ListSnapshotsResponse{Entries: entries, NextToken: nextToken}, nil
}

func listSnapshotEntries(snapshots []linodego.NFSSnapshot, handle volumeHandle) ([]*csi.ListSnapshotsResponse_Entry, error) {
	entries := make([]*csi.ListSnapshotsResponse_Entry, 0, len(snapshots))
	for i := range snapshots {
		snapshot, err := csiSnapshot(&snapshots[i], handle)
		if err != nil {
			return nil, err
		}
		entries = append(entries, &csi.ListSnapshotsResponse_Entry{Snapshot: snapshot})
	}
	return entries, nil
}

// csiSnapshot maps a Linode NFS snapshot to the CSI representation. It is shared
// by CreateSnapshot and ListSnapshots so both stay in sync.
func csiSnapshot(snapshot *linodego.NFSSnapshot, handle volumeHandle) (*csi.Snapshot, error) {
	if snapshot.Created == nil {
		return nil, status.Errorf(codes.Internal, "NFS snapshot %d does not have a created timestamp", snapshot.ID)
	}
	creationTime, err := util.ParseTimestamp(snapshot.Created)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to parse snapshot created time: %s", err)
	}

	return &csi.Snapshot{
		SizeBytes:      snapshot.SizeBytes,
		SnapshotId:     formatSnapshotHandle(handle.spaceID, handle.filesystemID, snapshot.ID),
		SourceVolumeId: formatVolumeHandle(handle.spaceID, handle.filesystemID),
		CreationTime:   creationTime,
		ReadyToUse:     snapshot.Status == linodego.NFSSnapshotStatusActive,
	}, nil
}

func csiCreateSnapshotResponse(snapshot *linodego.NFSSnapshot, handle volumeHandle) (*csi.CreateSnapshotResponse, error) {
	snapshotProto, err := csiSnapshot(snapshot, handle)
	if err != nil {
		return nil, err
	}
	return &csi.CreateSnapshotResponse{Snapshot: snapshotProto}, nil
}

// listSnapshotsStartingOffset decodes the CSI starting_token into an offset. An
// invalid token returns codes.Aborted so the caller restarts from the beginning.
func listSnapshotsStartingOffset(startingToken string) (int, error) {
	if startingToken == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(startingToken)
	if err != nil || offset < 0 {
		return 0, status.Errorf(codes.Aborted, "invalid starting token %q", startingToken)
	}
	return offset, nil
}

func paginateSnapshots(snapshots []linodego.NFSSnapshot, startingToken string, offset, maxEntries int) ([]linodego.NFSSnapshot, string, error) {
	if startingToken != "" && offset >= len(snapshots) {
		return nil, "", status.Errorf(codes.Aborted, "starting token %q is out of range", startingToken)
	}
	end := len(snapshots)
	nextToken := ""
	if maxEntries > 0 && maxEntries < len(snapshots)-offset {
		end = offset + maxEntries
		nextToken = strconv.Itoa(end)
	}

	return snapshots[offset:end], nextToken, nil
}
