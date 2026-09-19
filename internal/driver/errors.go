package driver

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Errors that are returned from RPC methods.
// They are defined here so they can be reused as lifecycle handlers are implemented.
var (
	errNilDriver                  = status.Error(codes.Internal, "nil driver")
	errNilMounter                 = status.Error(codes.Internal, "nil mounter")
	errNoVolumeName               = status.Error(codes.InvalidArgument, "volume name is required")
	errNoSnapshotName             = status.Error(codes.InvalidArgument, "snapshot name is required")
	errNoVolumeCapabilities       = status.Error(codes.InvalidArgument, "volume capabilities are required")
	errNoVolumeCapability         = status.Error(codes.InvalidArgument, "no volume capability set")
	errNoMountVolumeCapability    = status.Error(codes.InvalidArgument, "no mount volume capability set")
	errNoVolumeContextMountTarget = status.Error(codes.InvalidArgument, "mount-target is required in volume context")
	errNoVolumeID                 = status.Error(codes.InvalidArgument, "volume ID is not set")
	errNoVolumePath               = status.Error(codes.InvalidArgument, "volume path is not set")
	errNoTargetPath               = status.Error(codes.InvalidArgument, "target path is not set")
	errNoStagingTargetPath        = status.Error(codes.InvalidArgument, "staging target path is not set")
	errNotImplemented             = status.Error(codes.Unimplemented, "operation not implemented")
	errInvalidRole                = errors.New("invalid driver role")
	errInvalidAccessPolicyMode    = errors.New("invalid access policy mode")
	errInvalidMTLSMode            = status.Error(codes.InvalidArgument, "invalid mtls-mode value, must be one of: required, optional, disabled")
	errClusterVPCNotFound         = errors.New("cluster VPC not found")
	errLinodeClientNotFound       = errors.New("linode client not found or is nil")
	ErrTokenRequired              = errors.New("linode token required for controller role")
)

// errInternal is a convenience function to return a gRPC error with an
// INTERNAL status code.
func errInternal(format string, args ...any) error {
	return status.Errorf(codes.Internal, format, args...)
}

// errNotFound returns a gRPC error with a NOT_FOUND status code.
// It formats the error message using the provided format and arguments.
func errNotFound(format string, args ...any) error {
	return status.Errorf(codes.NotFound, format, args...)
}
