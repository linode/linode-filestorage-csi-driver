package driver

import (
	"context"
	"errors"
	"net/http"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/linode/linodego/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"

	linodeclient "github.com/linode/linode-filestorage-csi-driver/pkg/linode-client"
	"github.com/linode/linode-filestorage-csi-driver/pkg/util"
)

type ControllerServer struct {
	driver      *LinodeDriver
	client      linodeclient.LinodeClient
	volumeLocks *util.VolumeLocks
	csi.UnimplementedControllerServer
}

func NewControllerServer(ctx context.Context, driver *LinodeDriver, client linodeclient.LinodeClient, volumeLocks *util.VolumeLocks) (*ControllerServer, error) {
	klog.V(4).InfoS("creating controller server")
	if driver == nil {
		return nil, errNilDriver
	}
	if client == nil {
		return nil, errLinodeClientNotFound
	}
	return &ControllerServer{
		driver:      driver,
		client:      client,
		volumeLocks: volumeLocks,
	}, nil
}

func (s *ControllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	klog.V(4).InfoS("handling controller rpc", "method", "CreateVolume")

	if req.GetName() == "" {
		return nil, errNoVolumeName
	}
	if err := validateCreateVolumeCapabilities(req.GetVolumeCapabilities()); err != nil {
		return nil, err
	}
	snapshot, err := parseSnapshotContentSource(req.GetVolumeContentSource())
	if err != nil {
		return nil, err
	}

	params, err := parseCreateVolumeParameters(req.GetParameters())
	if err != nil {
		return nil, err
	}
	cluster, err := s.driver.metadata.Cluster(ctx)
	if err != nil {
		if errors.Is(err, errClusterVPCNotFound) {
			return nil, status.Error(codes.FailedPrecondition, "this driver requires VPC-backed IPv6 connectivity; cluster VPC not found")
		}
		return nil, status.Errorf(codes.FailedPrecondition, "resolve cluster metadata: %v", err)
	}
	params.region = cluster.Region

	space, err := s.resolveSpace(ctx, &params)
	if err != nil {
		return nil, err
	}

	existing, found, err := s.findExistingFilesystem(ctx, space.ID, req.GetName(), params.region)
	if err != nil {
		return nil, err
	}
	capacityBytes := requestedCapacityBytes(req.GetCapacityRange())

	if found {
		return s.handleExistingFilesystem(ctx, existing, &params, space, capacityBytes, snapshot)
	}

	// TEMP-DISABLED(access-policy): NFS Access Policy endpoints are not yet
	// implemented on the beta NFSaaS backend, so space/filesystem access-policy
	// calls are disabled below until support lands. Restore the commented code
	// to re-enable.
	var spacePolicy *linodego.NFSSpaceAccessPolicy
	//nolint:gocritic // intentionally preserved, not dead code, for restoring once Access Policy support lands
	/*
		spacePolicy, err = s.getSpaceAccessPolicy(ctx, space.ID)
		if err != nil {
			return nil, err
		}

		if err := s.ensureSpaceVPC(ctx, space.ID, cluster.VPCID, spacePolicy); err != nil {
			return nil, err
		}
	*/

	if snapshot != nil {
		// Snapshot restores target the cluster's region. Source-region validation is
		// deferred until cross-region CSI behavior is explicitly defined.
		return s.restoreFromSnapshot(ctx, req, *snapshot, space.ID, &params, capacityBytes, spacePolicy)
	}

	createOptions := linodego.NFSFilesystemCreateOptions{
		Label:            req.GetName(),
		Region:           params.region,
		ProtocolVersions: new([]linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4}),
	}
	if params.tags != nil {
		createOptions.Tags = new(params.tags)
	}
	filesystem, err := s.client.CreateNFSFilesystem(ctx, space.ID, createOptions)
	if err != nil {
		return nil, linodeError(err, "create NFS filesystem")
	}
	waitCtx, cancel := waitContext(ctx)
	defer cancel()
	filesystem, err = s.client.WaitForNFSFilesystemStatus(waitCtx, filesystem.SpaceID, filesystem.ID, linodego.NFSFilesystemStatusActive)
	if err != nil {
		return nil, linodeWaitError(err, "wait for NFS filesystem active")
	}

	if err := validateFilesystemMountTarget(filesystem); err != nil {
		return nil, err
	}
	// TEMP-DISABLED(access-policy): root-squash configuration requires the
	// filesystem access-policy API. Restore the commented code to re-enable.
	//nolint:gocritic // intentionally preserved, not dead code, for restoring once Access Policy support lands
	/*
		if params.squashPolicySet {
			if err := s.setInitialSquashPolicy(ctx, filesystem.SpaceID, filesystem.ID, params.squashPolicy); err != nil {
				return nil, err
			}
		}
	*/

	return &csi.CreateVolumeResponse{Volume: csiVolume(filesystem, capacityBytes, spaceMTLSMode(spacePolicy))}, nil
}

func (s *ControllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	klog.V(4).InfoS("handling controller rpc", "method", "DeleteVolume")

	if req.GetVolumeId() == "" {
		return nil, errNoVolumeID
	}
	handle, err := parseVolumeHandle(req.GetVolumeId())
	if err != nil {
		return nil, err
	}

	if err := s.client.DeleteNFSFilesystem(ctx, handle.spaceID, handle.filesystemID); err != nil {
		if linodego.IsNotFound(err) {
			return &csi.DeleteVolumeResponse{}, nil
		}
		return nil, linodeError(err, "delete NFS filesystem")
	}

	return &csi.DeleteVolumeResponse{}, nil
}

func (s *ControllerServer) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	klog.V(4).InfoS("handling controller rpc", "method", "ControllerPublishVolume")

	// TEMP-DISABLED(access-policy): depends on the filesystem access-policy API,
	// not yet implemented on the beta NFSaaS backend. Not advertising
	// PUBLISH_UNPUBLISH_VOLUME (capabilities.go) means the CO won't call this.
	// Restore the full implementation (see git history) once Access Policy
	// support lands.
	_ = req
	return nil, errNotImplemented
}

func (s *ControllerServer) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	klog.V(4).InfoS("handling controller rpc", "method", "ControllerUnpublishVolume")

	// TEMP-DISABLED(access-policy): depends on the filesystem access-policy API,
	// not yet implemented on the beta NFSaaS backend. Not advertising
	// PUBLISH_UNPUBLISH_VOLUME (capabilities.go) means the CO won't call this.
	// Restore the full implementation (see git history) once Access Policy
	// support lands.
	_ = req
	return nil, errNotImplemented
}

func (s *ControllerServer) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	klog.V(4).InfoS("handling controller rpc", "method", "ValidateVolumeCapabilities")

	if req.GetVolumeId() == "" {
		return nil, errNoVolumeID
	}
	if len(req.GetVolumeCapabilities()) == 0 {
		return nil, errNoVolumeCapabilities
	}

	handle, err := parseVolumeHandle(req.GetVolumeId())
	if err != nil {
		return nil, err
	}

	for _, capability := range req.GetVolumeCapabilities() {
		if supported, message := volumeCapabilitySupported(capability); !supported {
			return &csi.ValidateVolumeCapabilitiesResponse{Message: message}, nil
		}
	}

	filesystem, err := s.client.GetNFSFilesystem(ctx, handle.spaceID, handle.filesystemID)
	if err != nil {
		return nil, linodeError(err, "get NFS filesystem")
	}
	if err := validateFilesystemMountTarget(filesystem); err != nil {
		return nil, err
	}

	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeContext:      volumeContext(filesystem, ""),
			VolumeCapabilities: req.GetVolumeCapabilities(),
		},
	}, nil
}

func (s *ControllerServer) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	klog.V(4).InfoS("handling controller rpc", "method", "ControllerGetCapabilities")

	return &csi.ControllerGetCapabilitiesResponse{Capabilities: s.driver.controllerCaps}, nil
}

func (s *ControllerServer) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	klog.V(4).InfoS("handling controller rpc", "method", "ControllerExpandVolume")

	// Future implementation will translate requested capacity into a backend quota or share-size update.
	_ = req
	return nil, errNotImplemented
}

func (s *ControllerServer) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	klog.V(4).InfoS("handling controller rpc", "method", "GetCapacity")

	// Future implementation will surface backend capacity information once the API semantics are known.
	_ = req
	return nil, errNotImplemented
}

func (s *ControllerServer) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	klog.V(4).InfoS("handling controller rpc", "method", "ListVolumes")

	// Future implementation will enumerate filesystems visible to the driver.
	_ = req
	return nil, errNotImplemented
}

func (s *ControllerServer) ControllerGetVolume(ctx context.Context, req *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	klog.V(4).InfoS("handling controller rpc", "method", "ControllerGetVolume")

	if req.GetVolumeId() == "" {
		return nil, errNoVolumeID
	}

	handle, err := parseVolumeHandle(req.GetVolumeId())
	if err != nil {
		return nil, err
	}

	filesystem, err := s.client.GetNFSFilesystem(ctx, handle.spaceID, handle.filesystemID)
	if err != nil {
		return nil, linodeError(err, "get NFS filesystem")
	}
	if err := validateFilesystemMountTarget(filesystem); err != nil {
		return nil, err
	}

	// TEMP-DISABLED(access-policy): published-node status depends on the
	// filesystem access-policy API, not yet implemented on the beta NFSaaS
	// backend. Restore the commented code to re-enable.
	//nolint:gocritic // intentionally preserved, not dead code, for restoring once Access Policy support lands
	/*
		policy, err := s.client.GetNFSFilesystemAccessPolicy(ctx, handle.spaceID, handle.filesystemID)
		if err != nil {
			return nil, linodeError(err, "get NFS filesystem access policy")
		}
	*/

	return &csi.ControllerGetVolumeResponse{
		Volume: csiVolume(filesystem, 0, ""),
		Status: &csi.ControllerGetVolumeResponse_VolumeStatus{},
	}, nil
}

func (s *ControllerServer) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	klog.V(4).InfoS("handling controller rpc", "method", "CreateSnapshot")

	name := req.GetName()
	if name == "" {
		return nil, errNoSnapshotName
	}

	volumeID := req.GetSourceVolumeId()
	if volumeID == "" {
		return nil, errNoVolumeID
	}

	if acquired := s.volumeLocks.TryAcquire(volumeID); !acquired {
		return nil, status.Errorf(codes.Aborted, util.VolumeOperationAlreadyExistsFmt, volumeID)
	}
	defer s.volumeLocks.Release(volumeID)

	handle, err := parseVolumeHandle(volumeID)
	if err != nil {
		return nil, err
	}

	existingSnapshot, err := s.findSnapshotByLabel(ctx, handle, name)
	if err != nil {
		return nil, linodeError(err, "list NFS snapshots")
	}
	if existingSnapshot != nil {
		return csiCreateSnapshotResponse(existingSnapshot, handle)
	}

	snapshot, err := s.client.CreateNFSSnapshot(ctx, handle.spaceID, handle.filesystemID, linodego.NFSSnapshotCreateOptions{
		Label: name,
	})
	if err != nil && linodego.ErrHasStatus(err, http.StatusConflict) {
		existingSnapshot, lookupErr := s.findSnapshotByLabel(ctx, handle, name)
		if lookupErr == nil && existingSnapshot != nil {
			return csiCreateSnapshotResponse(existingSnapshot, handle)
		}
	}
	if err != nil {
		return nil, linodeError(err, "create NFS snapshot")
	}
	waitCtx, cancel := waitContext(ctx)
	defer cancel()
	snapshot, err = s.client.WaitForNFSSnapshotStatus(waitCtx, handle.spaceID, handle.filesystemID, snapshot.ID, linodego.NFSSnapshotStatusActive)
	if err != nil {
		return nil, linodeWaitError(err, "wait for NFS snapshot active")
	}

	response, err := csiCreateSnapshotResponse(snapshot, handle)
	if err != nil {
		return nil, err
	}
	klog.V(4).Infof("CreateSnapshot succeeded for volume %v, Backup ID: %v", volumeID, snapshot.ID)

	return response, nil
}

func (s *ControllerServer) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	klog.V(4).InfoS("handling controller rpc", "method", "DeleteSnapshot")

	snapshotID := req.GetSnapshotId()
	if snapshotID == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot id is required")
	}

	handle, err := parseSnapshotHandle(snapshotID)
	if err != nil {
		return nil, err
	}

	if err := s.client.DeleteNFSSnapshot(ctx, handle.spaceID, handle.filesystemID, handle.snapshotID); err != nil {
		if linodego.IsNotFound(err) {
			return &csi.DeleteSnapshotResponse{}, nil
		}
		return nil, linodeError(err, "delete NFS snapshot")
	}

	return &csi.DeleteSnapshotResponse{}, nil
}

func (s *ControllerServer) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	klog.V(4).InfoS("handling controller rpc", "method", "ListSnapshots")

	snapshotID := req.GetSnapshotId()
	sourceVolumeID := req.GetSourceVolumeId()
	maxEntries := int(req.GetMaxEntries())
	if maxEntries < 0 {
		return nil, status.Errorf(codes.InvalidArgument, "max entries must not be negative: %d", maxEntries)
	}
	if snapshotID == "" && sourceVolumeID == "" {
		// external-snapshotter v8.2.0 only calls ListSnapshots with snapshot_id.
		// Supporting an unfiltered request would require account-wide traversal.
		return nil, status.Error(codes.InvalidArgument, "snapshot id or source volume id is required")
	}
	if snapshotID != "" {
		return s.listSnapshotByID(ctx, snapshotID, sourceVolumeID)
	}
	return s.listSnapshotsBySourceVolume(ctx, sourceVolumeID, req.GetStartingToken(), maxEntries)
}
