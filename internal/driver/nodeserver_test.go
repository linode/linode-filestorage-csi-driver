package driver

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	metadataapi "github.com/linode/go-metadata"
	"go.uber.org/mock/gomock"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/utils/mount"

	"github.com/linode/linode-filestorage-csi-driver/mocks"
	mountmanager "github.com/linode/linode-filestorage-csi-driver/pkg/mount-manager"
)

func TestNodeGetInfo(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*mocks.MockInstanceMetadataClient)
		wantID   string
		wantCode codes.Code
	}{
		{
			name: "uses instance metadata client",
			setup: func(instanceClient *mocks.MockInstanceMetadataClient) {
				instanceClient.EXPECT().GetInstance(gomock.Any()).Return(&metadataapi.InstanceData{ID: 101, Region: "us-east"}, nil)
				instanceClient.EXPECT().GetNetwork(gomock.Any()).Return(testPrivateNetwork("10.0.0.12/24"), nil)
			},
			wantID: "101",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			instanceClient := mocks.NewMockInstanceMetadataClient(ctrl)
			if tt.setup != nil {
				tt.setup(instanceClient)
			}
			metadataSvc := metadataService{nodeName: "worker-a", instanceClient: instanceClient, kubeClient: mocks.NewMockKubeNodeClient(ctrl)}
			server := &NodeServer{driver: &LinodeDriver{metadata: metadataSvc}}

			response, err := server.NodeGetInfo(context.Background(), &csi.NodeGetInfoRequest{})
			if status.Code(err) != tt.wantCode {
				t.Fatalf("NodeGetInfo() code = %v, want %v", status.Code(err), tt.wantCode)
			}
			if tt.wantCode != codes.OK {
				return
			}
			if response.GetNodeId() != tt.wantID {
				t.Fatalf("NodeGetInfo() node id = %q, want %q", response.GetNodeId(), tt.wantID)
			}
		})
	}
}

func TestNodeGetCapabilities(t *testing.T) {
	tests := []struct {
		name     string
		caps     []*csi.NodeServiceCapability
		wantCaps []*csi.NodeServiceCapability
	}{
		{
			name: "returns configured capabilities",
			caps: []*csi.NodeServiceCapability{
				{Type: &csi.NodeServiceCapability_Rpc{Rpc: &csi.NodeServiceCapability_RPC{Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME}}},
			},
			wantCaps: []*csi.NodeServiceCapability{
				{Type: &csi.NodeServiceCapability_Rpc{Rpc: &csi.NodeServiceCapability_RPC{Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME}}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := &NodeServer{driver: &LinodeDriver{nodeCaps: tt.caps}}

			response, err := server.NodeGetCapabilities(context.Background(), &csi.NodeGetCapabilitiesRequest{})
			if err != nil {
				t.Fatalf("NodeGetCapabilities() error = %v", err)
			}
			if len(response.GetCapabilities()) != len(tt.wantCaps) {
				t.Fatalf("NodeGetCapabilities() count = %d, want %d", len(response.GetCapabilities()), len(tt.wantCaps))
			}
			for idx, want := range tt.wantCaps {
				if got := response.GetCapabilities()[idx].GetRpc().GetType(); got != want.GetRpc().GetType() {
					t.Fatalf("NodeGetCapabilities()[%d] = %v, want %v", idx, got, want.GetRpc().GetType())
				}
			}
		})
	}
}

func TestNodeServerUnimplementedRPCs(t *testing.T) {
	tests := []struct {
		name string
		call func(*NodeServer) error
	}{
		{name: "NodeStageVolume", call: func(server *NodeServer) error {
			_, err := server.NodeStageVolume(context.Background(), &csi.NodeStageVolumeRequest{})
			return err
		}},
		{name: "NodeExpandVolume", call: func(server *NodeServer) error {
			_, err := server.NodeExpandVolume(context.Background(), &csi.NodeExpandVolumeRequest{})
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := &NodeServer{driver: &LinodeDriver{}}

			err := tt.call(server)
			if status.Code(err) != codes.Unimplemented {
				t.Fatalf("%s code = %v, want %v", tt.name, status.Code(err), codes.Unimplemented)
			}
		})
	}
}

func testPrivateNetwork(prefix string) *metadataapi.NetworkData {
	return &metadataapi.NetworkData{
		IPv4: metadataapi.IPv4Data{Private: []netip.Prefix{netip.MustParsePrefix(prefix)}},
	}
}

func TestNodePublishVolume(t *testing.T) {
	tests := []struct {
		name               string
		req                *csi.NodePublishVolumeRequest
		resp               *csi.NodePublishVolumeResponse
		expectMounterCalls func(m *mocks.MockMounter)
		expectedError      error
	}{
		{
			name: "Existing read only mount point",
			req: &csi.NodePublishVolumeRequest{
				VolumeId: "vol-123",
				VolumeContext: map[string]string{
					"mount-target": "nfs.server.linode.com:/fs-id",
				},
				TargetPath:        "/tmp/target",
				StagingTargetPath: "/tmp/staging",
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{FsType: "nfs"},
					},
				},
				Readonly: true,
			},
			resp: &csi.NodePublishVolumeResponse{},
			expectMounterCalls: func(m *mocks.MockMounter) {
				m.EXPECT().IsLikelyNotMountPoint("/tmp/target").Return(false, nil)
			},
			expectedError: nil,
		},
		{
			name: "No volume ID",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:          "",
				TargetPath:        "/tmp/target",
				StagingTargetPath: "/tmp/staging",
				VolumeContext: map[string]string{
					"mount-target": "nfs.server.linode.com:/fs-id",
				},
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{FsType: "nfs"},
					},
				}},
			expectedError: errNoVolumeID,
		},
		{
			name: "No staging target path",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:          "vol-123",
				TargetPath:        "/tmp/target",
				StagingTargetPath: "",
				VolumeContext: map[string]string{
					"mount-target": "nfs.server.linode.com:/fs-id",
				},
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{FsType: "nfs"},
					},
				}},
			expectedError: errNoStagingTargetPath,
		},
		{
			name: "No target path",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:          "vol-123",
				TargetPath:        "",
				StagingTargetPath: "/tmp/staging",
				VolumeContext: map[string]string{
					"mount-target": "nfs.server.linode.com:/fs-id",
				},
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{FsType: "nfs"},
					},
				}},
			expectedError: errNoTargetPath,
		},
		{
			name: "No volume capability",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:          "vol-123",
				TargetPath:        "/tmp/target",
				StagingTargetPath: "/tmp/staging",
				VolumeContext: map[string]string{
					"mount-target": "nfs.server.linode.com:/fs-id",
				},
			},
			expectedError: errNoVolumeCapability,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockMounter := mocks.NewMockMounter(ctrl)
			mockExec := mocks.NewMockExecutor(ctrl)
			if tt.expectMounterCalls != nil {
				tt.expectMounterCalls(mockMounter)
			}
			ns := &NodeServer{
				driver: &LinodeDriver{},
				mounter: &mountmanager.SafeFormatAndMount{
					SafeFormatAndMount: &mount.SafeFormatAndMount{
						Interface: mockMounter,
						Exec:      mockExec,
					},
				},
			}
			returnedResp, err := ns.NodePublishVolume(context.Background(), tt.req)
			if err != nil && !errors.Is(err, tt.expectedError) {
				t.Errorf("NodePublishVolume error = %v, wantErr %v", err, tt.expectedError)
			}
			if !reflect.DeepEqual(returnedResp, tt.resp) {
				t.Errorf("NodeServer.NodePublishVolume() = %v, want %v", returnedResp, tt.resp)
			}
		})
	}
}

func TestNodeUnpublishVolume(t *testing.T) {
	tests := []struct {
		name               string
		req                *csi.NodeUnpublishVolumeRequest
		resp               *csi.NodeUnpublishVolumeResponse
		expectMounterCalls func(m *mocks.MockMounter)
		expectedError      error
	}{
		{
			name: "Path does not exist",
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   "vol-123",
				TargetPath: "/mnt/target",
			},
			resp:               &csi.NodeUnpublishVolumeResponse{},
			expectMounterCalls: func(m *mocks.MockMounter) {},
			expectedError:      nil,
		},
		{
			name: "No volume ID",
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   "",
				TargetPath: "/tmp/target",
			},
			expectedError: errNoVolumeID,
		},
		{
			name: "No target path",
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   "vol-123",
				TargetPath: "",
			},
			expectedError: errNoTargetPath,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockMounter := mocks.NewMockMounter(ctrl)
			mockExec := mocks.NewMockExecutor(ctrl)
			if tt.expectMounterCalls != nil {
				tt.expectMounterCalls(mockMounter)
			}
			ns := &NodeServer{
				driver: &LinodeDriver{},
				mounter: &mountmanager.SafeFormatAndMount{
					SafeFormatAndMount: &mount.SafeFormatAndMount{
						Interface: mockMounter,
						Exec:      mockExec,
					},
				},
			}
			returnedResp, err := ns.NodeUnpublishVolume(context.Background(), tt.req)
			if err != nil && !errors.Is(err, tt.expectedError) {
				t.Errorf("NodeUnpublishVolume error = %v, wantErr %v", err, tt.expectedError)
			}
			if !reflect.DeepEqual(returnedResp, tt.resp) {
				t.Errorf("NodeServer.NodeUnpublishVolume() = %v, want %v", returnedResp, tt.resp)
			}
		})
	}
}

func TestNodeUnstageVolume(t *testing.T) {
	tests := []struct {
		name          string
		req           *csi.NodeUnstageVolumeRequest
		resp          *csi.NodeUnstageVolumeResponse
		expectedError error
	}{
		{
			name: "Path does not exist",
			req: &csi.NodeUnstageVolumeRequest{
				VolumeId:          "1001-volkey",
				StagingTargetPath: "/mnt/staging",
			},
			resp:          &csi.NodeUnstageVolumeResponse{},
			expectedError: nil,
		},
		{
			name: "No volume ID",
			req: &csi.NodeUnstageVolumeRequest{
				VolumeId:          "",
				StagingTargetPath: "/mnt/staging",
			},
			expectedError: errNoVolumeID,
		},
		{
			name: "No target path",
			req: &csi.NodeUnstageVolumeRequest{
				VolumeId:          "1001-volkey",
				StagingTargetPath: "",
			},
			expectedError: errNoStagingTargetPath,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockMounter := mocks.NewMockMounter(ctrl)
			mockExec := mocks.NewMockExecutor(ctrl)

			ns := &NodeServer{
				driver: &LinodeDriver{},
				mounter: &mountmanager.SafeFormatAndMount{
					SafeFormatAndMount: &mount.SafeFormatAndMount{
						Interface: mockMounter,
						Exec:      mockExec,
					},
				},
			}
			returnedResp, err := ns.NodeUnstageVolume(context.Background(), tt.req)
			if err != nil && !errors.Is(err, tt.expectedError) {
				t.Errorf("NodeUnstageVolume error = %v, wantErr %v", err, tt.expectedError)
			}
			if !reflect.DeepEqual(returnedResp, tt.resp) {
				t.Errorf("NodeServer.NodeUnstageVolume() = %v, want %v", returnedResp, tt.resp)
			}
		})
	}
}

func TestNodeGetVolumeStats(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStatfs := func(path string, stat *unix.Statfs_t) error {
		switch path {
		case "/valid/path":
			stat.Blocks = 1000
			stat.Bfree = 200
			stat.Bavail = 150
			stat.Files = 500
			stat.Ffree = 100
			stat.Bsize = 4096
			return nil
		case "/not/mounted":
			return unix.EIO
		case "/not/exist":
			return unix.ENOENT
		default:
			return errors.New("internal error")
		}
	}

	unixStatfs = mockStatfs

	testCases := []struct {
		name        string
		volumeID    string
		volumePath  string
		expectedErr error
		expectedRes *csi.NodeGetVolumeStatsResponse
	}{
		{
			name:        "Valid request with healthy volume",
			volumeID:    "valid-volume",
			volumePath:  "/valid/path",
			expectedErr: nil,
			expectedRes: &csi.NodeGetVolumeStatsResponse{
				Usage: []*csi.VolumeUsage{
					{
						Available: 150 * 4096,
						Total:     1000 * 4096,
						Used:      (1000 - 200) * 4096,
						Unit:      csi.VolumeUsage_BYTES,
					},
					{
						Available: 100,
						Total:     500,
						Used:      500 - 100,
						Unit:      csi.VolumeUsage_INODES,
					},
				},
				VolumeCondition: &csi.VolumeCondition{
					Abnormal: false,
					Message:  "healthy",
				},
			},
		},
		{
			name:        "Request with empty volume ID",
			volumeID:    "",
			volumePath:  "/valid/path",
			expectedErr: errNoVolumeID,
			expectedRes: nil,
		},
		{
			name:        "Request with empty volume path",
			volumeID:    "valid-volume",
			volumePath:  "",
			expectedErr: errNoVolumePath,
			expectedRes: nil,
		},
		{
			name:        "Filesystem not mounted",
			volumeID:    "not-mounted-volume",
			volumePath:  "/not/mounted",
			expectedErr: nil,
			expectedRes: &csi.NodeGetVolumeStatsResponse{
				VolumeCondition: &csi.VolumeCondition{
					Abnormal: true,
					Message:  "failed to get stats: input/output error",
				},
			},
		},
		{
			name:        "Volume path does not exist",
			volumeID:    "non-existent-volume",
			volumePath:  "/not/exist",
			expectedErr: errNotFound("volume path not found: no such file or directory"),
			expectedRes: nil,
		},
		{
			name:        "Internal error during Statfs call",
			volumeID:    "internal-error-volume",
			volumePath:  "/internal/error",
			expectedErr: errInternal("failed to get stats: internal error"),
			expectedRes: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			req := &csi.NodeGetVolumeStatsRequest{
				VolumeId:   tc.volumeID,
				VolumePath: tc.volumePath,
			}

			mockMounter := mocks.NewMockMounter(ctrl)
			mockExec := mocks.NewMockExecutor(ctrl)
			ns := &NodeServer{
				driver: &LinodeDriver{},
				mounter: &mountmanager.SafeFormatAndMount{
					SafeFormatAndMount: &mount.SafeFormatAndMount{
						Interface: mockMounter,
						Exec:      mockExec,
					},
				},
			}
			resp, err := ns.NodeGetVolumeStats(ctx, req)

			if err != nil && !errors.Is(err, tc.expectedErr) {
				t.Errorf("NodeGetVolumeStats error = %v, wantErr %v", err, tc.expectedErr)
			}

			if !reflect.DeepEqual(resp, tc.expectedRes) {
				t.Errorf("NodeServer.NodeGetVolumeStats() = %v, want %v", resp, tc.expectedRes)
			}
		})
	}
}
