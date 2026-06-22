package driver

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/linode/linodego/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/utils/ptr"
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
				storageClassParamSpaceID:    " nfss-123abc ",
				storageClassParamTags:       "tag-a, tag-b,, ",
				storageClassParamRootSquash: string(linodego.NFSRootSquashModeRootSquash),
			},
			want: createVolumeParameters{
				spaceID:       "nfss-123abc",
				tags:          []string{"tag-a", "tag-b"},
				rootSquash:    linodego.NFSRootSquashModeRootSquash,
				rootSquashSet: true,
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
				storageClassParamSpaceID:    "nfss-123abc",
				storageClassParamSpaceLabel: "shared-space",
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "invalid root squash",
			params: map[string]string{
				storageClassParamSpaceID:    "nfss-123abc",
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
		{name: "valid", volumeID: " nfss-123abc/fs-12345678 ", want: volumeHandle{spaceID: "nfss-123abc", filesystemID: "fs-12345678"}},
		{name: "empty", volumeID: "", wantCode: codes.InvalidArgument},
		{name: "missing filesystem", volumeID: "nfss-123abc/", wantCode: codes.InvalidArgument},
		{name: "missing space", volumeID: "/fs-12345678", wantCode: codes.InvalidArgument},
		{name: "too many parts", volumeID: "nfss-123abc/fs-12345678/extra", wantCode: codes.InvalidArgument},
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

func TestVolumeID(t *testing.T) {
	if got := volumeID("nfss-123abc", "fs-12345678"); got != "nfss-123abc/fs-12345678" {
		t.Fatalf("volumeID() = %q", got)
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
		ID:          "fs-12345678",
		SpaceID:     "nfss-123abc",
		Region:      "us-east",
		MountTarget: "nfss-123abc.nfs.us-east.linode.com:/fs-12345678",
	}

	tests := []struct {
		name string
		want map[string]string
	}{
		{
			name: "owned",
			want: map[string]string{
				volumeContextSpaceID:      "nfss-123abc",
				volumeContextFilesystemID: "fs-12345678",
				volumeContextMountTarget:  "nfss-123abc.nfs.us-east.linode.com:/fs-12345678",
				volumeContextRegion:       "us-east",
			},
		},
		{
			name: "unowned",
			want: map[string]string{
				volumeContextSpaceID:      "nfss-123abc",
				volumeContextFilesystemID: "fs-12345678",
				volumeContextMountTarget:  "nfss-123abc.nfs.us-east.linode.com:/fs-12345678",
				volumeContextRegion:       "us-east",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := volumeContext(filesystem); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("volumeContext() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCSIVolume(t *testing.T) {
	filesystem := &linodego.NFSFilesystem{
		ID:          "fs-12345678",
		SpaceID:     "nfss-123abc",
		Region:      "us-east",
		MountTarget: "nfss-123abc.nfs.us-east.linode.com:/fs-12345678",
	}

	volume := csiVolume(filesystem, 1024)
	if volume.GetVolumeId() != "nfss-123abc/fs-12345678" {
		t.Fatalf("csiVolume() volume id = %q", volume.GetVolumeId())
	}
	if volume.GetCapacityBytes() != 1024 {
		t.Fatalf("csiVolume() capacity = %d", volume.GetCapacityBytes())
	}
	if len(volume.GetVolumeContext()) != 4 {
		t.Fatalf("csiVolume() context = %#v", volume.GetVolumeContext())
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

func TestStringSliceHelpers(t *testing.T) {
	tests := []struct {
		name      string
		left      []string
		right     []string
		value     string
		contains  bool
		append    []string
		slicesAre bool
	}{
		{name: "present equal", left: []string{"vpc-a", "vpc-b"}, right: []string{"vpc-a", "vpc-b"}, value: "vpc-b", contains: true, append: []string{"vpc-a", "vpc-b"}, slicesAre: true},
		{name: "absent unequal", left: []string{"vpc-a", "vpc-b"}, right: []string{"vpc-b", "vpc-a"}, value: "vpc-c", append: []string{"vpc-a", "vpc-b", "vpc-c"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := slices.Contains(tt.left, tt.value); got != tt.contains {
				t.Fatalf("slices.Contains() = %v, want %v", got, tt.contains)
			}
			if got := appendUniqueString(tt.left, tt.value); !reflect.DeepEqual(got, tt.append) {
				t.Fatalf("appendUniqueString() = %#v, want %#v", got, tt.append)
			}
			if got := slices.Equal(tt.left, tt.right); got != tt.slicesAre {
				t.Fatalf("slices.Equal() = %v, want %v", got, tt.slicesAre)
			}
		})
	}
}

func TestPtrToBool(t *testing.T) {
	for _, value := range []bool{false, true} {
		t.Run(http.StatusText(http.StatusOK), func(t *testing.T) {
			got := ptr.To(value)
			if got == nil || *got != value {
				t.Fatalf("ptr.To() = %v, want %v", got, value)
			}
		})
	}
}

func TestFilesystemPolicyRootSquashUpdate(t *testing.T) {
	policy := &linodego.NFSFilesystemAccessPolicy{
		Label:      "policy-a",
		Enabled:    false,
		LinodeIDs:  []int{101},
		RootSquash: linodego.NFSRootSquashModeNone,
		Protocols:  []linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4},
	}

	got := filesystemPolicyRootSquashUpdate(policy, linodego.NFSRootSquashModeRootSquash)
	want := linodego.NFSFilesystemAccessPolicyUpdateOptions{
		Label:      "policy-a",
		Enabled:    ptr.To(false),
		LinodeIDs:  []int{101},
		RootSquash: linodego.NFSRootSquashModeRootSquash,
		Protocols:  []linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filesystemPolicyRootSquashUpdate() = %#v, want %#v", got, want)
	}
}
