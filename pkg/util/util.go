package util

import (
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func ParseTimestamp(timestamp *time.Time) (*timestamppb.Timestamp, error) {
	// ptypes.TimestampProto is deprecated; use timestamppb.New
	tp := timestamppb.New(*timestamp)
	if tp == nil {
		return nil, status.Errorf(codes.Internal, "failed to convert timestamp %v", timestamp)
	}
	if err := tp.CheckValid(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to convert timestamp %v: %v", timestamp, err.Error())
	}
	return tp, nil
}
