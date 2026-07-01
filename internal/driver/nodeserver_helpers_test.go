package driver

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/status"
	"k8s.io/utils/mount"

	"github.com/linode/linode-filestorage-csi-driver/mocks"
	mountmanager "github.com/linode/linode-filestorage-csi-driver/pkg/mount-manager"
)

// compareGRPCErrors compares two gRPC errors for equality.
//
// Parameters:
//   - t: The testing.T instance to log errors.
//   - got: The error received during the test.
//   - want: The expected error for comparison.
//
// Returns: void

func compareGRPCErrors(t *testing.T, got, want error) {
	t.Helper()

	if (got == nil) != (want == nil) {
		t.Errorf("Error presence mismatch: got %v, want %v", got, want)
		return
	}

	if got == nil && want == nil {
		return // Both are nil, so they're equal
	}

	gotStatus, ok := status.FromError(got)
	if !ok {
		t.Errorf("Got error is not a gRPC status error: %v", got)
		return
	}

	wantStatus, ok := status.FromError(want)
	if !ok {
		t.Errorf("Want error is not a gRPC status error: %v", want)
		return
	}

	if gotStatus.Code() != wantStatus.Code() {
		t.Errorf("Status code mismatch: got %v, want %v", gotStatus.Code(), wantStatus.Code())
		return
	}

	if gotStatus.Message() != wantStatus.Message() {
		t.Errorf("Error message mismatch: got %q, want %q", gotStatus.Message(), wantStatus.Message())
	}
}

func TestNodeServer_ensureMountPoint(t *testing.T) {
	tests := []struct {
		name              string
		stagingTargetPath string
		mntExpects        func(m *mocks.MockMounter)
		fsExpects         func(m *mocks.MockFileSystem)
		want              bool
		wantErr           error
	}{
		{
			name:              "Success - Staging target path is a mount point (expect false)",
			stagingTargetPath: "/mnt/staging",
			mntExpects: func(m *mocks.MockMounter) {
				// Returning false because that means the target path is already a mount point
				m.EXPECT().IsLikelyNotMountPoint(gomock.Any()).Return(false, nil)
			},
			fsExpects: func(m *mocks.MockFileSystem) {},
			want:      false,
			wantErr:   nil,
		},
		{
			name:              "Success -  Mount point didn't exist so we created a new mount point",
			stagingTargetPath: "/mnt/staging",
			mntExpects: func(m *mocks.MockMounter) {
				// Returning false because that means the target path is already a mount point
				m.EXPECT().IsLikelyNotMountPoint(gomock.Any()).Return(false, fmt.Errorf("mount point doesn't exist"))
			},
			fsExpects: func(m *mocks.MockFileSystem) {
				m.EXPECT().IsNotExist(gomock.Any()).Return(true)
				m.EXPECT().MkdirAll(gomock.Any(), gomock.Any()).Return(nil)
			},
			want:    false,
			wantErr: errInternal("Failed to create directory (/mnt/staging): couldn't create directory"),
		},
		{
			name:              "Error -  mount point check fails and error is not IsNotExist",
			stagingTargetPath: "/mnt/staging",
			mntExpects: func(m *mocks.MockMounter) {
				// Returning false because that means the target path is already a mount point
				m.EXPECT().IsLikelyNotMountPoint(gomock.Any()).Return(false, fmt.Errorf("some error"))
			},
			fsExpects: func(m *mocks.MockFileSystem) {
				m.EXPECT().IsNotExist(gomock.Any()).Return(false)
			},
			want:    true,
			wantErr: errInternal("Unknown error when checking mount point (\"/mnt/staging\"): some error"),
		},
		{
			name:              "Error -  Mount point didn't exist and ran into error when create a new mount point or directory",
			stagingTargetPath: "/mnt/staging",
			mntExpects: func(m *mocks.MockMounter) {
				// Returning false because that means the target path is already a mount point
				m.EXPECT().IsLikelyNotMountPoint(gomock.Any()).Return(false, fmt.Errorf("some error"))
			},
			fsExpects: func(m *mocks.MockFileSystem) {
				m.EXPECT().IsNotExist(gomock.Any()).Return(true)
				m.EXPECT().MkdirAll(gomock.Any(), gomock.Any()).Return(fmt.Errorf("couldn't create directory"))
			},
			want:    true,
			wantErr: errInternal("Failed to create directory (\"/mnt/staging\"): couldn't create directory"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockMounter := mocks.NewMockMounter(ctrl)
			mockFileSystem := mocks.NewMockFileSystem(ctrl)

			if tt.mntExpects != nil {
				tt.mntExpects(mockMounter)
			}
			if tt.fsExpects != nil {
				tt.fsExpects(mockFileSystem)
			}

			ns := &NodeServer{
				mounter: &mountmanager.SafeFormatAndMount{
					SafeFormatAndMount: &mount.SafeFormatAndMount{
						Interface: mockMounter,
						Exec:      nil,
					},
				},
			}
			got, err := ns.ensureMountPoint(tt.stagingTargetPath, mockFileSystem)
			if err != nil {
				compareGRPCErrors(t, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("NodeServer.ensureMountPoint() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNodeServer_nodePublishVolume(t *testing.T) {
	tests := []struct {
		name       string
		mntExpects func(m *mocks.MockMounter)
		req        *csi.NodePublishVolumeRequest
		resp       *csi.NodePublishVolumeResponse
		wantErr    error
	}{
		{
			name: "Success",
			req: &csi.NodePublishVolumeRequest{
				VolumeId: "vol-123",
				VolumeContext: map[string]string{
					"mount-target": "nfs.server.linode.com:/fs-id",
				},
				TargetPath:        "/tmp/target",
				StagingTargetPath: "/tmp/staging",
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{
							FsType:     "nfs4",
							MountFlags: []string{"vers=4", "proto=tcp", "retrans"},
						},
					},
				},
				Readonly: true,
			},
			resp: &csi.NodePublishVolumeResponse{},
			mntExpects: func(m *mocks.MockMounter) {
				m.EXPECT().Mount("/tmp/staging", "/tmp/target", "nfs4", []string{"bind", "vers=4", "proto=tcp", "retrans", "ro"}).Return(nil)
			},
			wantErr: nil,
		},
		{
			name: "Failure cleans up mount point",
			req: &csi.NodePublishVolumeRequest{
				VolumeId: "vol-123",
				VolumeContext: map[string]string{
					"mount-target": "nfs.server.linode.com:/fs-id",
				},
				TargetPath:        "/tmp/target",
				StagingTargetPath: "/tmp/staging",
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{
							FsType:     "nfs4",
							MountFlags: []string{"vers=4", "proto=tcp", "retrans"},
						},
					},
				},
			},
			mntExpects: func(m *mocks.MockMounter) {
				m.EXPECT().Mount("/tmp/staging", "/tmp/target", "nfs4", []string{"bind", "vers=4", "proto=tcp", "retrans"}).Return(errors.New("mount failed"))
			},
			wantErr: errInternal("NodePublishVolume could not mount %s at %s: %v", "/tmp/staging", "/tmp/target", errors.New("mount failed")),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockMounter := mocks.NewMockMounter(ctrl)

			if tt.mntExpects != nil {
				tt.mntExpects(mockMounter)
			}

			ns := &NodeServer{
				mounter: &mountmanager.SafeFormatAndMount{
					SafeFormatAndMount: &mount.SafeFormatAndMount{
						Interface: mockMounter,
						Exec:      nil,
					},
				},
			}
			returnedResp, err := ns.nodePublishVolume(tt.req)
			if err != nil {
				compareGRPCErrors(t, err, tt.wantErr)
			}
			if !reflect.DeepEqual(returnedResp, tt.resp) {
				t.Errorf("NodeServer.nodePublishVolume() = %v, want %v", returnedResp, tt.resp)
			}
		})
	}
}
