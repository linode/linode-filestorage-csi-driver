package driver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/linode/linodego/v2"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestParseCreateVolumeParameters(t *testing.T) {
	tests := []struct {
		name     string
		params   map[string]string
		want     createVolumeParameters
		wantCode codes.Code
	}{
		{
			name: "space id with tags",
			params: map[string]string{
				storageClassParamSpaceID:    " 123 ",
				storageClassParamTags:       "tag-a, tag-b,, ",
				storageClassParamRootSquash: string(linodego.NFSSquashPolicyRootSquash),
			},
			want: createVolumeParameters{
				spaceID:         123,
				tags:            []string{"tag-a", "tag-b"},
				squashPolicy:    linodego.NFSSquashPolicyRootSquash,
				squashPolicySet: true,
			},
		},
		{
			name: "space label",
			params: map[string]string{
				storageClassParamSpaceLabel: "shared-space",
			},
			want: createVolumeParameters{spaceLabel: "shared-space"},
		},
		{
			name:     "missing space selector",
			params:   map[string]string{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "mutually exclusive space selectors",
			params: map[string]string{
				storageClassParamSpaceID:    "123",
				storageClassParamSpaceLabel: "shared-space",
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "invalid root squash",
			params: map[string]string{
				storageClassParamSpaceID:    "123",
				storageClassParamRootSquash: "invalid",
			},
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCreateVolumeParameters(tt.params)
			if status.Code(err) != tt.wantCode {
				t.Fatalf("parseCreateVolumeParameters() code = %v, want %v", status.Code(err), tt.wantCode)
			}
			if tt.wantCode != codes.OK {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseCreateVolumeParameters() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseVolumeHandle(t *testing.T) {
	tests := []struct {
		name     string
		volumeID string
		want     volumeHandle
		wantCode codes.Code
	}{
		{name: "valid", volumeID: " 123/456 ", want: volumeHandle{spaceID: 123, filesystemID: 456}},
		{name: "empty", volumeID: "", wantCode: codes.InvalidArgument},
		{name: "missing filesystem", volumeID: "123/", wantCode: codes.InvalidArgument},
		{name: "missing space", volumeID: "/456", wantCode: codes.InvalidArgument},
		{name: "too many parts", volumeID: "123/456/extra", wantCode: codes.InvalidArgument},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseVolumeHandle(tt.volumeID)
			if status.Code(err) != tt.wantCode {
				t.Fatalf("parseVolumeHandle() code = %v, want %v", status.Code(err), tt.wantCode)
			}
			if tt.wantCode != codes.OK {
				return
			}
			if got != tt.want {
				t.Fatalf("parseVolumeHandle() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestFormatSnapshotHandle(t *testing.T) {
	if got := formatSnapshotHandle(123, 456, 789); got != "123/456/789" {
		t.Fatalf("formatSnapshotHandle() = %q, want %q", got, "123/456/789")
	}
}

func TestParseSnapshotHandle(t *testing.T) {
	tests := []struct {
		name       string
		snapshotID string
		want       snapshotHandle
		wantCode   codes.Code
	}{
		{name: "valid", snapshotID: " 123/456/789 ", want: snapshotHandle{spaceID: 123, filesystemID: 456, snapshotID: 789}},
		{name: "empty", snapshotID: "", wantCode: codes.InvalidArgument},
		{name: "missing snapshot", snapshotID: "123/456/", wantCode: codes.InvalidArgument},
		{name: "missing filesystem", snapshotID: "123//789", wantCode: codes.InvalidArgument},
		{name: "missing space", snapshotID: "/456/789", wantCode: codes.InvalidArgument},
		{name: "too few parts", snapshotID: "123/456", wantCode: codes.InvalidArgument},
		{name: "too many parts", snapshotID: "123/456/789/extra", wantCode: codes.InvalidArgument},
		{name: "invalid space", snapshotID: "space/456/789", wantCode: codes.InvalidArgument},
		{name: "invalid filesystem", snapshotID: "123/fs/789", wantCode: codes.InvalidArgument},
		{name: "invalid snapshot", snapshotID: "123/456/snap", wantCode: codes.InvalidArgument},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSnapshotHandle(tt.snapshotID)
			if status.Code(err) != tt.wantCode {
				t.Fatalf("parseSnapshotHandle() code = %v, want %v", status.Code(err), tt.wantCode)
			}
			if tt.wantCode != codes.OK {
				return
			}
			if got != tt.want {
				t.Fatalf("parseSnapshotHandle() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseVolumeHandleAndNodeID(t *testing.T) {
	tests := []struct {
		name     string
		volumeID string
		nodeID   string
		want     volumeHandle
		wantID   int
		wantCode codes.Code
	}{
		{name: "valid", volumeID: "123/456", nodeID: "202", want: volumeHandle{spaceID: 123, filesystemID: 456}, wantID: 202},
		{name: "invalid volume id", volumeID: "123", nodeID: "202", wantCode: codes.InvalidArgument},
		{name: "invalid node id", volumeID: "123/456", nodeID: "worker-a", wantCode: codes.InvalidArgument},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handle, linodeID, err := parseVolumeHandleAndNodeID(tt.volumeID, tt.nodeID)
			if status.Code(err) != tt.wantCode {
				t.Fatalf("parseVolumeHandleAndNodeID() code = %v, want %v", status.Code(err), tt.wantCode)
			}
			if tt.wantCode != codes.OK {
				return
			}
			if handle != tt.want {
				t.Fatalf("parseVolumeHandleAndNodeID() handle = %#v, want %#v", handle, tt.want)
			}
			if linodeID != tt.wantID {
				t.Fatalf("parseVolumeHandleAndNodeID() linodeID = %d, want %d", linodeID, tt.wantID)
			}
		})
	}
}

func TestGetFilesystemPolicyForVolumeAndNode(t *testing.T) {
	tests := []struct {
		name           string
		volumeID       string
		nodeID         string
		setup          func(controllerTestEnv) *linodego.NFSFilesystemAccessPolicy
		wantHandle     volumeHandle
		wantID         int
		wantCode       codes.Code
		wantNotFound   bool
		wantSamePolicy bool
	}{
		{
			name:       "success",
			volumeID:   testVolumeID,
			nodeID:     "202",
			wantHandle: volumeHandle{spaceID: 123, filesystemID: 456},
			wantID:     202,
			setup: func(env controllerTestEnv) *linodego.NFSFilesystemAccessPolicy {
				policy := &linodego.NFSFilesystemAccessPolicy{FilesystemID: 456, Enabled: true, LinodeACL: []linodego.NFSFilesystemAccessPolicyLinode{{ID: 202}}}
				env.client.EXPECT().GetNFSFilesystemAccessPolicy(gomock.Any(), 123, 456).Return(policy, nil)
				return policy
			},
			wantSamePolicy: true,
		},
		{
			name:         "not found is preserved",
			volumeID:     testVolumeID,
			nodeID:       "202",
			wantNotFound: true,
			setup: func(env controllerTestEnv) *linodego.NFSFilesystemAccessPolicy {
				env.client.EXPECT().GetNFSFilesystemAccessPolicy(gomock.Any(), 123, 456).Return(nil, linodeAPIError(http.StatusNotFound))
				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newControllerTestEnv(t)
			wantPolicy := tt.setup(env)

			handle, linodeID, gotPolicy, err := env.server.getFilesystemPolicyForVolumeAndNode(t.Context(), tt.volumeID, tt.nodeID)
			if tt.wantNotFound {
				if !linodego.IsNotFound(err) {
					t.Fatalf("getFilesystemPolicyForVolumeAndNode() error = %v, want not found", err)
				}
				return
			}
			if status.Code(err) != tt.wantCode {
				t.Fatalf("getFilesystemPolicyForVolumeAndNode() code = %v, want %v", status.Code(err), tt.wantCode)
			}
			if tt.wantCode != codes.OK {
				return
			}
			if handle != tt.wantHandle {
				t.Fatalf("getFilesystemPolicyForVolumeAndNode() handle = %#v, want %#v", handle, tt.wantHandle)
			}
			if linodeID != tt.wantID {
				t.Fatalf("getFilesystemPolicyForVolumeAndNode() linodeID = %d, want %d", linodeID, tt.wantID)
			}
			if tt.wantSamePolicy && gotPolicy != wantPolicy {
				t.Fatalf("getFilesystemPolicyForVolumeAndNode() policy = %#v, want %#v", gotPolicy, wantPolicy)
			}
		})
	}
}

func TestRequestedCapacityBytes(t *testing.T) {
	tests := []struct {
		name          string
		capacityRange *csi.CapacityRange
		want          int64
	}{
		{name: "nil", want: 0},
		{name: "required", capacityRange: &csi.CapacityRange{RequiredBytes: 1024, LimitBytes: 2048}, want: 1024},
		{name: "limit fallback", capacityRange: &csi.CapacityRange{LimitBytes: 2048}, want: 2048},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requestedCapacityBytes(tt.capacityRange); got != tt.want {
				t.Fatalf("requestedCapacityBytes() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestVolumeCapabilitySupported(t *testing.T) {
	tests := []struct {
		name          string
		capability    *csi.VolumeCapability
		wantSupported bool
		wantMessage   string
	}{
		{name: "nil", wantMessage: "no volume capability set"},
		{name: "block", capability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}}, wantMessage: "only mount volume capabilities are supported"},
		{name: "unknown mode", capability: mountCapability(csi.VolumeCapability_AccessMode_UNKNOWN), wantMessage: "volume access mode is required"},
		{name: "single node writer", capability: mountCapability(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER), wantSupported: true},
		{name: "single node reader only", capability: mountCapability(csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY), wantSupported: true},
		{name: "multi node reader only", capability: mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY), wantSupported: true},
		{name: "multi node single writer", capability: mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER), wantSupported: true},
		{name: "multi node multi writer", capability: mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER), wantSupported: true},
		{name: "single node single writer", capability: mountCapability(csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER), wantSupported: true},
		{name: "single node multi writer", capability: mountCapability(csi.VolumeCapability_AccessMode_SINGLE_NODE_MULTI_WRITER), wantSupported: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			supported, message := volumeCapabilitySupported(tt.capability)
			if supported != tt.wantSupported || message != tt.wantMessage {
				t.Fatalf("volumeCapabilitySupported() = (%v, %q), want (%v, %q)", supported, message, tt.wantSupported, tt.wantMessage)
			}
		})
	}
}

func TestValidateCreateVolumeCapabilities(t *testing.T) {
	tests := []struct {
		name         string
		capabilities []*csi.VolumeCapability
		wantCode     codes.Code
	}{
		{name: "empty", wantCode: codes.InvalidArgument},
		{name: "unsupported", capabilities: []*csi.VolumeCapability{{AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}}}, wantCode: codes.InvalidArgument},
		{name: "supported", capabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCreateVolumeCapabilities(tt.capabilities)
			if status.Code(err) != tt.wantCode {
				t.Fatalf("validateCreateVolumeCapabilities() code = %v, want %v", status.Code(err), tt.wantCode)
			}
		})
	}
}

func TestVolumeContext(t *testing.T) {
	filesystem := &linodego.NFSFilesystem{
		ID:              456,
		SpaceID:         123,
		Region:          "us-east",
		MountTargetFQDN: new("prod-7b.nfs.us-east.linode.com:/pvc-abc-1c8"),
	}

	tests := []struct {
		name     string
		mtlsMode linodego.NFSMTLSMode
		want     map[string]string
	}{
		{
			name: "owned",
			want: map[string]string{
				volumeContextSpaceID:      "123",
				volumeContextFilesystemID: "456",
				volumeContextMountTarget:  "prod-7b.nfs.us-east.linode.com:/pvc-abc-1c8",
				volumeContextRegion:       "us-east",
			},
		},
		{
			name:     "with mtls mode",
			mtlsMode: linodego.NFSMTLSModeRequired,
			want: map[string]string{
				volumeContextSpaceID:       "123",
				volumeContextFilesystemID:  "456",
				volumeContextMountTarget:   "prod-7b.nfs.us-east.linode.com:/pvc-abc-1c8",
				volumeContextRegion:        "us-east",
				volumeContextSpaceMTLSMode: string(linodego.NFSMTLSModeRequired),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := volumeContext(filesystem, tt.mtlsMode); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("volumeContext() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCSIVolume(t *testing.T) {
	filesystem := &linodego.NFSFilesystem{
		ID:              456,
		SpaceID:         123,
		Region:          "us-east",
		MountTargetFQDN: new("prod-7b.nfs.us-east.linode.com:/pvc-abc-1c8"),
	}

	volume := csiVolume(filesystem, 1024, "")
	if volume.GetVolumeId() != "123/456" {
		t.Fatalf("csiVolume() volume id = %q", volume.GetVolumeId())
	}
	if volume.GetCapacityBytes() != 1024 {
		t.Fatalf("csiVolume() capacity = %d", volume.GetCapacityBytes())
	}
	if len(volume.GetVolumeContext()) != 4 {
		t.Fatalf("csiVolume() context = %#v", volume.GetVolumeContext())
	}

	volumeWithMTLS := csiVolume(filesystem, 1024, linodego.NFSMTLSModeOptional)
	if got := volumeWithMTLS.GetVolumeContext()[volumeContextSpaceMTLSMode]; got != string(linodego.NFSMTLSModeOptional) {
		t.Fatalf("csiVolume() mtls mode = %q", got)
	}
}

func TestLinodeError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode codes.Code
	}{
		{name: "nil"},
		{name: "not found", err: linodeAPIError(http.StatusNotFound), wantCode: codes.NotFound},
		{name: "bad request", err: linodeAPIError(http.StatusBadRequest), wantCode: codes.InvalidArgument},
		{name: "unprocessable entity", err: linodeAPIError(http.StatusUnprocessableEntity), wantCode: codes.InvalidArgument},
		{name: "unauthorized", err: linodeAPIError(http.StatusUnauthorized), wantCode: codes.PermissionDenied},
		{name: "forbidden", err: linodeAPIError(http.StatusForbidden), wantCode: codes.PermissionDenied},
		{name: "conflict", err: linodeAPIError(http.StatusConflict), wantCode: codes.FailedPrecondition},
		{name: "too many requests", err: linodeAPIError(http.StatusTooManyRequests), wantCode: codes.Unavailable},
		{name: "bad gateway", err: linodeAPIError(http.StatusBadGateway), wantCode: codes.Unavailable},
		{name: "service unavailable", err: linodeAPIError(http.StatusServiceUnavailable), wantCode: codes.Unavailable},
		{name: "gateway timeout", err: linodeAPIError(http.StatusGatewayTimeout), wantCode: codes.Unavailable},
		{name: "fallback", err: errors.New("boom"), wantCode: codes.Internal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := linodeError(tt.err, "test")
			if status.Code(err) != tt.wantCode {
				t.Fatalf("linodeError() code = %v, want %v", status.Code(err), tt.wantCode)
			}
		})
	}
}

func TestLinodeWaitError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode codes.Code
	}{
		{name: "nil"},
		{name: "context canceled", err: context.Canceled, wantCode: codes.Canceled},
		{name: "context deadline exceeded", err: context.DeadlineExceeded, wantCode: codes.DeadlineExceeded},
		{name: "unavailable", err: linodeAPIError(http.StatusServiceUnavailable), wantCode: codes.Unavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := linodeWaitError(tt.err, "wait")
			if status.Code(err) != tt.wantCode {
				t.Fatalf("linodeWaitError() code = %v, want %v", status.Code(err), tt.wantCode)
			}
		})
	}
}

func TestNormalizeLabel(t *testing.T) {
	got := normalizeLabel("Sanity_TEST.42")
	if want := "sanity_test.42"; got != want {
		t.Fatalf("normalizeLabel() = %q, want %q", got, want)
	}
}

func TestTruncateNFSLabelToMaxBytes(t *testing.T) {
	tests := []struct {
		name  string
		label string
		want  string
	}{
		{name: "preserves label at the limit", label: strings.Repeat("a", 63), want: strings.Repeat("a", 63)},
		{name: "truncates a label over the limit", label: strings.Repeat("a", 70), want: strings.Repeat("a", 63)},
		{name: "trims a single trailing hyphen left by truncation", label: strings.Repeat("a", 62) + "-x", want: strings.Repeat("a", 62)},
		{name: "trims a run of trailing hyphens left by truncation", label: strings.Repeat("a", 61) + "--x", want: strings.Repeat("a", 61)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateNFSLabelToMaxBytes(tt.label); got != tt.want {
				t.Fatalf("truncateNFSLabelToMaxBytes(%q) = %q, want %q", tt.label, got, tt.want)
			}
		})
	}
}

func TestListOptionsForExactFields(t *testing.T) {
	options, err := listOptionsForExactFields(map[string]string{"label": "pvc-abc", "region": "us-east"})
	if err != nil {
		t.Fatalf("listOptionsForExactFields() error = %v", err)
	}

	var filter map[string]string
	if err := json.Unmarshal([]byte(options.Filter), &filter); err != nil {
		t.Fatalf("unmarshal filter: %v", err)
	}
	want := map[string]string{"label": "pvc-abc", "region": "us-east"}
	if !reflect.DeepEqual(filter, want) {
		t.Fatalf("listOptionsForExactFields() filter = %#v, want %#v", filter, want)
	}
}

func TestFilesystemPolicySquashPolicyUpdate(t *testing.T) {
	policy := &linodego.NFSFilesystemAccessPolicy{
		Label:        "policy-a",
		Enabled:      false,
		LinodeACL:    []linodego.NFSFilesystemAccessPolicyLinode{{ID: 101}},
		SquashPolicy: linodego.NFSSquashPolicyNone,
		Protocols:    []linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4},
	}

	got := filesystemPolicySquashPolicyUpdate(policy, linodego.NFSSquashPolicyRootSquash)
	want := linodego.NFSFilesystemAccessPolicyUpdateOptions{
		Label:        new("policy-a"),
		Enabled:      new(false),
		LinodeIDs:    new([]int{101}),
		SquashPolicy: new(linodego.NFSSquashPolicyRootSquash),
		Protocols:    new([]linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4}),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filesystemPolicySquashPolicyUpdate() = %#v, want %#v", got, want)
	}
}
