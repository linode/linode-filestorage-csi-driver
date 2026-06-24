package driver

import (
	"context"
	"errors"
	"slices"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/linode/linodego/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"

	linodeclient "github.com/linode/linode-filestorage-csi-driver/pkg/linode-client"
)

type ControllerServer struct {
	driver *LinodeDriver
	client linodeclient.LinodeClient
	csi.UnimplementedControllerServer
}

func NewControllerServer(ctx context.Context, driver *LinodeDriver, client linodeclient.LinodeClient) (*ControllerServer, error) {
	klog.V(4).InfoS("creating controller server")
	if driver == nil {
		return nil, errNilDriver
	}
	if client == nil {
		return nil, errLinodeClientNotFound
	}
	return &ControllerServer{driver: driver, client: client}, nil
}

func (s *ControllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	klog.V(4).InfoS("handling controller rpc", "method", "CreateVolume")

	if req.GetName() == "" {
		return nil, errNoVolumeName
	}
	if err := validateCreateVolumeCapabilities(req.GetVolumeCapabilities()); err != nil {
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
	spacePolicy, err := s.getSpaceAccessPolicy(ctx, space.ID)
	if err != nil {
		return nil, err
	}

	capacityBytes := requestedCapacityBytes(req.GetCapacityRange())
	existing, found, err := s.findExistingFilesystem(ctx, space.ID, req.GetName(), params.region)
	if err != nil {
		return nil, err
	}
	if found {
		if err := s.validateExistingFilesystem(ctx, existing, &params); err != nil {
			return nil, err
		}
		return &csi.CreateVolumeResponse{Volume: csiVolume(existing, capacityBytes, spacePolicy.MTLSMode)}, nil
	}

	if err := s.ensureSpaceVPC(ctx, space.ID, cluster.VPCID, spacePolicy); err != nil {
		return nil, err
	}

	filesystem, err := s.client.CreateNFSFilesystem(ctx, space.ID, linodego.NFSFilesystemCreateOptions{
		Label:            req.GetName(),
		Region:           params.region,
		ProtocolVersions: []linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4},
		Tags:             params.tags,
	})
	if err != nil {
		return nil, linodeError(err, "create NFS filesystem")
	}

	if params.rootSquashSet {
		if err := s.setInitialRootSquash(ctx, filesystem.SpaceID, filesystem.ID, params.rootSquash); err != nil {
			return nil, err
		}
	}

	return &csi.CreateVolumeResponse{Volume: csiVolume(filesystem, capacityBytes, spacePolicy.MTLSMode)}, nil
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

	if req.GetVolumeId() == "" {
		return nil, errNoVolumeID
	}
	if req.GetNodeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "node id is required")
	}
	if supported, message := volumeCapabilitySupported(req.GetVolumeCapability()); !supported {
		return nil, status.Error(codes.InvalidArgument, message)
	}

	handle, linodeID, policy, err := s.getFilesystemPolicyForVolumeAndNode(ctx, req.GetVolumeId(), req.GetNodeId())
	if err != nil {
		if status.Code(err) == codes.InvalidArgument {
			return nil, err
		}
		return nil, linodeError(err, "get NFS filesystem access policy")
	}
	if slices.Contains(policy.LinodeIDs, linodeID) {
		if policy.Enabled {
			return &csi.ControllerPublishVolumeResponse{}, nil
		}
		if _, err := s.client.UpdateNFSFilesystemAccessPolicy(ctx, handle.spaceID, handle.filesystemID, filesystemPolicyUpdate(policy, true, policy.LinodeIDs)); err != nil {
			return nil, linodeError(err, "update NFS filesystem access policy")
		}
		return &csi.ControllerPublishVolumeResponse{}, nil
	}

	policy.LinodeIDs = append(policy.LinodeIDs, linodeID)
	if _, err := s.client.UpdateNFSFilesystemAccessPolicy(ctx, handle.spaceID, handle.filesystemID, filesystemPolicyUpdate(policy, true, policy.LinodeIDs)); err != nil {
		return nil, linodeError(err, "update NFS filesystem access policy")
	}

	return &csi.ControllerPublishVolumeResponse{}, nil
}

func (s *ControllerServer) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	klog.V(4).InfoS("handling controller rpc", "method", "ControllerUnpublishVolume")

	if req.GetVolumeId() == "" {
		return nil, errNoVolumeID
	}
	if req.GetNodeId() == "" {
		return &csi.ControllerUnpublishVolumeResponse{}, nil
	}

	handle, linodeID, policy, err := s.getFilesystemPolicyForVolumeAndNode(ctx, req.GetVolumeId(), req.GetNodeId())
	if err != nil {
		if linodego.IsNotFound(err) {
			return &csi.ControllerUnpublishVolumeResponse{}, nil
		}
		if status.Code(err) == codes.InvalidArgument {
			return nil, err
		}
		return nil, linodeError(err, "get NFS filesystem access policy")
	}

	if !slices.Contains(policy.LinodeIDs, linodeID) {
		return &csi.ControllerUnpublishVolumeResponse{}, nil
	}

	updatedIDs := slices.DeleteFunc(policy.LinodeIDs, func(existing int) bool {
		return existing == linodeID
	})
	if _, err := s.client.UpdateNFSFilesystemAccessPolicy(ctx, handle.spaceID, handle.filesystemID, filesystemPolicyUpdate(policy, policy.Enabled, updatedIDs)); err != nil {
		return nil, linodeError(err, "update NFS filesystem access policy")
	}

	return &csi.ControllerUnpublishVolumeResponse{}, nil
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

	policy, err := s.client.GetNFSFilesystemAccessPolicy(ctx, handle.spaceID, handle.filesystemID)
	if err != nil {
		return nil, linodeError(err, "get NFS filesystem access policy")
	}

	return &csi.ControllerGetVolumeResponse{
		Volume: csiVolume(filesystem, 0, ""),
		Status: csiControllerVolumeStatus(policy),
	}, nil
}

func (s *ControllerServer) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	klog.V(4).InfoS("handling controller rpc", "method", "CreateSnapshot")

	// Future implementation will create a backend-native snapshot when the managed file storage API supports it.
	_ = req
	return nil, errNotImplemented
}

func (s *ControllerServer) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	klog.V(4).InfoS("handling controller rpc", "method", "DeleteSnapshot")

	// Future implementation will delete a previously created backend snapshot.
	_ = req
	return nil, errNotImplemented
}

func (s *ControllerServer) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	klog.V(4).InfoS("handling controller rpc", "method", "ListSnapshots")

	// Future implementation will list backend snapshots or filter a single snapshot by ID.
	_ = req
	return nil, errNotImplemented
}
