package driver

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Errors that are returned from RPC methods.
// They are defined here so they can be reused as lifecycle handlers are implemented.
//
//nolint:unused // This scaffold intentionally defines the full error set before all handlers use it.
var (
	errNilDriver            = status.Error(codes.Internal, "nil driver")
	errNoVolumeName         = status.Error(codes.InvalidArgument, "volume name is required")
	errNoVolumeCapabilities = status.Error(codes.InvalidArgument, "volume capabilities are required")
	errNoVolumeCapability   = status.Error(codes.InvalidArgument, "no volume capability set")
	errNoVolumeID           = status.Error(codes.InvalidArgument, "volume id is not set")
	errNoVolumePath         = status.Error(codes.InvalidArgument, "volume path is not set")
	errNoTargetPath         = status.Error(codes.InvalidArgument, "target path is not set")
	errNoStagingTargetPath  = status.Error(codes.InvalidArgument, "staging target path is not set")
	errNotImplemented       = status.Error(codes.Unimplemented, "operation not implemented")
	errInvalidRole          = errors.New("invalid driver role")
)
