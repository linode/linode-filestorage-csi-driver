package util

import (
	"math"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const bytesInGiB = 1024.0 * 1024.0 * 1024.0

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

// BytesToGiB converts bytes to GiB, rounding up
func BytesToGiB(bytes int64) int {
	// use a minimum of 1 GiB
	if bytes < bytesInGiB {
		return 1
	}

	return int(math.Ceil(float64(bytes) / bytesInGiB))
}
