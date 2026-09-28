package util

// TODO: Restore NFS snapshot helpers when upstream linodego exposes the API.
/*
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
*/
