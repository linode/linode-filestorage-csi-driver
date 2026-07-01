package driver

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	metadataapi "github.com/linode/go-metadata"
	"github.com/linode/linodego/v2"
	"go.uber.org/mock/gomock"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/utils/mount"

	"github.com/linode/linode-filestorage-csi-driver/mocks"
	mountmanager "github.com/linode/linode-filestorage-csi-driver/pkg/mount-manager"
	"github.com/linode/linode-filestorage-csi-driver/pkg/util"
)

func defaultNodeServer(t *testing.T) (*NodeServer, *mocks.MockMounter) {
	t.Helper()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mounter := mocks.NewMockMounter(ctrl)
	return &NodeServer{
		driver: &LinodeDriver{},
		mounter: &mountmanager.SafeFormatAndMount{
			SafeFormatAndMount: &mount.SafeFormatAndMount{
				Interface: mounter,
				Exec:      mocks.NewMockExecutor(ctrl),
			},
		},
		volumeLocks: util.NewVolumeLocks(),
	}, mounter
}

func TestNewNodeServer(t *testing.T) {
	tests := []struct {
		name           string
		wantErr        error
		driver         *LinodeDriver
		mounter        *mountmanager.SafeFormatAndMount
		wantNodeServer *NodeServer
	}{
		{
			name:    "nil driver",
			wantErr: errNilDriver,
			mounter: &mountmanager.SafeFormatAndMount{},
		},
		{
			name:    "nil mounter",
			wantErr: errNilMounter,
			driver:  &LinodeDriver{},
		},
		{
			name: "success",
			wantNodeServer: &NodeServer{
				driver:      &LinodeDriver{},
				mounter:     &mountmanager.SafeFormatAndMount{},
				volumeLocks: util.NewVolumeLocks(),
			},
			driver:  &LinodeDriver{},
			mounter: &mountmanager.SafeFormatAndMount{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodeServer, err := NewNodeServer(context.Background(), tt.driver, tt.mounter, util.NewVolumeLocks())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewNodeServer() code = %v, want %v", status.Code(err), tt.wantErr)
			}
			if !reflect.DeepEqual(nodeServer, tt.wantNodeServer) {
				t.Fatalf("NewNodeServer() nodeserver = %v, want %v", nodeServer, tt.wantNodeServer)
			}
		})
	}
}

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
			defer ctrl.Finish()
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

func TestNodeStageVolume(t *testing.T) {
	newRequest := func(stagingTargetPath string, volumeContext, secrets map[string]string, mountFlags ...string) *csi.NodeStageVolumeRequest {
		return &csi.NodeStageVolumeRequest{
			VolumeId:          "nfss-123abc/fs-12345678",
			StagingTargetPath: stagingTargetPath,
			VolumeContext:     volumeContext,
			Secrets:           secrets,
			VolumeCapability: &csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{
						FsType:     "nfs",
						MountFlags: mountFlags,
					},
				},
			},
		}
	}

	tests := []struct {
		name               string
		req                *csi.NodeStageVolumeRequest
		setup              func(*NodeServer)
		expectMounterCalls func(*mocks.MockMounter)
		wantCode           codes.Code
	}{
		{
			name: "missing volume id is invalid",
			req: &csi.NodeStageVolumeRequest{
				StagingTargetPath: t.TempDir(),
				VolumeContext: map[string]string{
					volumeContextMountTarget: "nfs.server.linode.com:/fs-id",
				},
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{FsType: "nfs"},
					},
				},
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "missing staging target path is invalid",
			req: &csi.NodeStageVolumeRequest{
				VolumeId: "nfss-123abc/fs-12345678",
				VolumeContext: map[string]string{
					volumeContextMountTarget: "nfs.server.linode.com:/fs-id",
				},
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{FsType: "nfs"},
					},
				},
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "missing volume capability is invalid",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          "nfss-123abc/fs-12345678",
				StagingTargetPath: t.TempDir(),
				VolumeContext: map[string]string{
					volumeContextMountTarget: "nfs.server.linode.com:/fs-id",
				},
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "non-mount volume capability is invalid",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          "nfss-123abc/fs-12345678",
				StagingTargetPath: t.TempDir(),
				VolumeContext: map[string]string{
					volumeContextMountTarget: "nfs.server.linode.com:/fs-id",
				},
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Block{
						Block: &csi.VolumeCapability_BlockVolume{},
					},
				},
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "mounts staging target from volume context",
			req: newRequest(
				t.TempDir(),
				map[string]string{volumeContextMountTarget: "nfs.server.linode.com:/fs-id"},
				nil,
				"hard",
				"nconnect=8",
			),
			expectMounterCalls: func(m *mocks.MockMounter) {
				gomock.InOrder(
					m.EXPECT().IsLikelyNotMountPoint(gomock.Any()).Return(true, nil),
					m.EXPECT().Mount("nfs.server.linode.com:/fs-id", gomock.Any(), "nfs4", []string{"hard", "nconnect=8"}).Return(nil),
				)
			},
			wantCode: codes.OK,
		},
		{
			name: "optional mtls without node mtls support falls back to plain mount",
			req: newRequest(
				t.TempDir(),
				map[string]string{
					volumeContextMountTarget:   "nfs.server.linode.com:/fs-id",
					volumeContextSpaceMTLSMode: string(linodego.NFSMTLSModeOptional),
				},
				nil,
				"hard",
			),
			expectMounterCalls: func(m *mocks.MockMounter) {
				gomock.InOrder(
					m.EXPECT().IsLikelyNotMountPoint(gomock.Any()).Return(true, nil),
					m.EXPECT().Mount("nfs.server.linode.com:/fs-id", gomock.Any(), "nfs4", []string{"hard", "xprtsec=mtls"}).Return(errors.New("mtls unavailable")),
					m.EXPECT().Mount("nfs.server.linode.com:/fs-id", gomock.Any(), "nfs4", []string{"hard"}).Return(nil),
				)
			},
			wantCode: codes.OK,
		},
		{
			name: "optional mtls succeeds on first tls mount",
			req: newRequest(
				t.TempDir(),
				map[string]string{
					volumeContextMountTarget:   "nfs.server.linode.com:/fs-id",
					volumeContextSpaceMTLSMode: string(linodego.NFSMTLSModeOptional),
				},
				nil,
				"hard",
			),
			expectMounterCalls: func(m *mocks.MockMounter) {
				gomock.InOrder(
					m.EXPECT().IsLikelyNotMountPoint(gomock.Any()).Return(true, nil),
					m.EXPECT().Mount("nfs.server.linode.com:/fs-id", gomock.Any(), "nfs4", []string{"hard", "xprtsec=mtls"}).Return(nil),
				)
			},
			wantCode: codes.OK,
		},
		{
			name: "optional mtls returns error when tls and plain mounts fail",
			req: newRequest(
				t.TempDir(),
				map[string]string{
					volumeContextMountTarget:   "nfs.server.linode.com:/fs-id",
					volumeContextSpaceMTLSMode: string(linodego.NFSMTLSModeOptional),
				},
				nil,
				"hard",
			),
			expectMounterCalls: func(m *mocks.MockMounter) {
				gomock.InOrder(
					m.EXPECT().IsLikelyNotMountPoint(gomock.Any()).Return(true, nil),
					m.EXPECT().Mount("nfs.server.linode.com:/fs-id", gomock.Any(), "nfs4", []string{"hard", "xprtsec=mtls"}).Return(errors.New("mtls unavailable")),
					m.EXPECT().Mount("nfs.server.linode.com:/fs-id", gomock.Any(), "nfs4", []string{"hard"}).Return(errors.New("plain mount failed")),
				)
			},
			wantCode: codes.Internal,
		},
		{
			name: "required mtls adds tls mount option",
			req: newRequest(
				t.TempDir(),
				map[string]string{
					volumeContextMountTarget:   "nfs.server.linode.com:/fs-id",
					volumeContextSpaceMTLSMode: string(linodego.NFSMTLSModeRequired),
				},
				nil,
				"hard",
			),
			expectMounterCalls: func(m *mocks.MockMounter) {
				gomock.InOrder(
					m.EXPECT().IsLikelyNotMountPoint(gomock.Any()).Return(true, nil),
					m.EXPECT().Mount("nfs.server.linode.com:/fs-id", gomock.Any(), "nfs4", []string{"hard", "xprtsec=mtls"}).Return(nil),
				)
			},
			wantCode: codes.OK,
		},
		{
			name: "already staged mount is a no-op",
			req: newRequest(
				t.TempDir(),
				map[string]string{volumeContextMountTarget: "nfs.server.linode.com:/fs-id"},
				nil,
			),
			expectMounterCalls: func(m *mocks.MockMounter) {
				m.EXPECT().IsLikelyNotMountPoint(gomock.Any()).Return(false, nil)
				m.EXPECT().List().AnyTimes().Return([]mount.MountPoint{{
					Device: "nfs.server.linode.com:/fs-id",
					Type:   "nfs4",
				}}, nil)
			},
			wantCode: codes.OK,
		},
		{
			name: "volume lock contention returns aborted",
			req: newRequest(
				t.TempDir(),
				map[string]string{volumeContextMountTarget: "nfs.server.linode.com:/fs-id"},
				nil,
			),
			setup: func(ns *NodeServer) {
				ns.volumeLocks.TryAcquire("nfss-123abc/fs-12345678")
			},
			wantCode: codes.Aborted,
		},
		{
			name: "missing mount target is invalid",
			req: newRequest(
				t.TempDir(),
				map[string]string{},
				nil,
			),
			wantCode: codes.InvalidArgument,
		},
		{
			name: "unknown mtls mode is invalid",
			req: newRequest(
				t.TempDir(),
				map[string]string{
					volumeContextMountTarget:   "nfs.server.linode.com:/fs-id",
					volumeContextSpaceMTLSMode: "surprising",
				},
				nil,
			),
			wantCode: codes.InvalidArgument,
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
				volumeLocks: util.NewVolumeLocks(),
			}
			if tt.setup != nil {
				tt.setup(ns)
			}

			resp, err := ns.NodeStageVolume(context.Background(), tt.req)
			if status.Code(err) != tt.wantCode {
				t.Fatalf("NodeStageVolume() code = %v, want %v (err=%v)", status.Code(err), tt.wantCode, err)
			}
			if tt.wantCode == codes.OK {
				if resp == nil {
					t.Fatal("NodeStageVolume() response = nil, want non-nil")
				}
				return
			}
			if resp != nil {
				t.Fatalf("NodeStageVolume() response = %v, want nil", resp)
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
			ns, mounter := defaultNodeServer(t)
			if tt.expectMounterCalls != nil {
				tt.expectMounterCalls(mounter)
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
			ns, mounter := defaultNodeServer(t)
			if tt.expectMounterCalls != nil {
				tt.expectMounterCalls(mounter)
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
			ns, _ := defaultNodeServer(t)
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

			ns, _ := defaultNodeServer(t)
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
