package driver

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/linode/linodego/v2"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"

	"github.com/linode/linode-filestorage-csi-driver/mocks"
)

const testVolumeID = "nfss-123abc/fs-12345678"

type controllerTestEnv struct {
	client *mocks.MockLinodeClient
	kube   *mocks.MockKubeNodeClient
	server *ControllerServer
}

func newControllerTestEnv(t *testing.T) controllerTestEnv {
	t.Helper()

	ctrl := gomock.NewController(t)
	client := mocks.NewMockLinodeClient(ctrl)
	kubeClient := mocks.NewMockKubeNodeClient(ctrl)
	metadataSvc := metadataService{kubeClient: kubeClient, linodeClient: client}
	return controllerTestEnv{
		client: client,
		kube:   kubeClient,
		server: &ControllerServer{
			driver: &LinodeDriver{metadata: metadataSvc},
			client: client,
		},
	}
}

func TestCreateVolume(t *testing.T) {
	tests := []struct {
		name     string
		request  *csi.CreateVolumeRequest
		setup    func(*testing.T, controllerTestEnv)
		wantCode codes.Code
		assert   func(t *testing.T, response *csi.CreateVolumeResponse)
	}{
		{
			name: "creates filesystem",
			request: &csi.CreateVolumeRequest{
				Name:          "pvc-abc",
				CapacityRange: &csi.CapacityRange{RequiredBytes: 1024},
				Parameters: map[string]string{
					storageClassParamSpaceID:    "nfss-123abc",
					storageClassParamTags:       "tag-a, tag-b",
					storageClassParamRootSquash: string(linodego.NFSRootSquashModeRootSquash),
				},
				VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
			},
			setup: func(t *testing.T, env controllerTestEnv) {
				t.Helper()

				filesystemOptions, err := listOptionsForExactFields(map[string]string{"label": "pvc-abc", "region": "us-east"})
				if err != nil {
					t.Fatalf("list options: %v", err)
				}

				env.kube.EXPECT().ListNodes(gomock.Any()).Return(&corev1.NodeList{Items: []corev1.Node{
					*newTestNode("worker-a", "us-east", "10.0.0.20", "linode://202"),
				}}, nil)
				env.client.EXPECT().
					GetNFSSpace(gomock.Any(), "nfss-123abc").
					Return(&linodego.NFSSpace{ID: "nfss-123abc", Label: "prod-space"}, nil)
				env.client.EXPECT().
					ListNFSFilesystems(gomock.Any(), "nfss-123abc", gomock.Eq(filesystemOptions)).
					Return(nil, nil)
				env.client.EXPECT().ListInterfaces(gomock.Any(), 202, gomock.Nil()).Return([]linodego.LinodeInterface{
					{VPC: &linodego.VPCInterface{VPCID: 123456}},
				}, nil)
				env.client.EXPECT().GetNFSSpaceAccessPolicy(gomock.Any(), "nfss-123abc").Return(&linodego.NFSSpaceAccessPolicy{
					Label:   "space-policy",
					Enabled: true,
				}, nil)
				env.client.EXPECT().UpdateNFSSpaceAccessPolicy(gomock.Any(), "nfss-123abc", gomock.Eq(linodego.NFSSpaceAccessPolicyUpdateOptions{
					Label:   "space-policy",
					Enabled: boolPtr(true),
					VPCIDs:  []string{"123456"},
				})).Return(&linodego.NFSSpaceAccessPolicy{VPCIDs: []string{"123456"}}, nil)
				env.client.EXPECT().
					CreateNFSFilesystem(gomock.Any(), "nfss-123abc", gomock.Eq(linodego.NFSFilesystemCreateOptions{
						Label:            "pvc-abc",
						Region:           "us-east",
						ProtocolVersions: []linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4},
						Tags:             []string{"tag-a", "tag-b"},
					})).
					Return(&linodego.NFSFilesystem{
						ID:          "fs-12345678",
						SpaceID:     "nfss-123abc",
						Label:       "pvc-abc",
						Region:      "us-east",
						MountTarget: "nfss-123abc.nfs.us-east.linode.com:/fs-12345678",
					}, nil)
				env.client.EXPECT().
					GetNFSFilesystemAccessPolicy(gomock.Any(), "nfss-123abc", "fs-12345678").
					Return(&linodego.NFSFilesystemAccessPolicy{FilesystemID: "fs-12345678", Enabled: false, RootSquash: linodego.NFSRootSquashModeNone}, nil)
				env.client.EXPECT().
					UpdateNFSFilesystemAccessPolicy(gomock.Any(), "nfss-123abc", "fs-12345678", gomock.Eq(linodego.NFSFilesystemAccessPolicyUpdateOptions{
						Enabled:    boolPtr(false),
						RootSquash: linodego.NFSRootSquashModeRootSquash,
					})).
					Return(&linodego.NFSFilesystemAccessPolicy{FilesystemID: "fs-12345678"}, nil)
			},
			assert: func(t *testing.T, response *csi.CreateVolumeResponse) {
				t.Helper()

				volume := response.GetVolume()
				if volume.GetVolumeId() != testVolumeID {
					t.Fatalf("unexpected volume id %q", volume.GetVolumeId())
				}
				if volume.GetCapacityBytes() != 1024 {
					t.Fatalf("unexpected capacity %d", volume.GetCapacityBytes())
				}
				wantContext := map[string]string{
					volumeContextSpaceID:       "nfss-123abc",
					volumeContextFilesystemID:  "fs-12345678",
					volumeContextMountTarget:   "nfss-123abc.nfs.us-east.linode.com:/fs-12345678",
					volumeContextRegion:        "us-east",
					volumeContextOwnedByDriver: volumeContextOwnedValue,
				}
				if !reflect.DeepEqual(volume.GetVolumeContext(), wantContext) {
					t.Fatalf("unexpected volume context %#v", volume.GetVolumeContext())
				}
			},
		},
		{
			name: "returns existing compatible filesystem",
			request: &csi.CreateVolumeRequest{
				Name:               "pvc-abc",
				Parameters:         map[string]string{storageClassParamSpaceID: "nfss-123abc", storageClassParamTags: "tag-a"},
				VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
			},
			setup: func(t *testing.T, env controllerTestEnv) {
				t.Helper()

				filesystemOptions, err := listOptionsForExactFields(map[string]string{"label": "pvc-abc", "region": "us-east"})
				if err != nil {
					t.Fatalf("list options: %v", err)
				}

				env.kube.EXPECT().ListNodes(gomock.Any()).Return(&corev1.NodeList{Items: []corev1.Node{
					*newTestNode("worker-a", "us-east", "10.0.0.20", "linode://202"),
				}}, nil)
				env.client.EXPECT().ListInterfaces(gomock.Any(), 202, gomock.Nil()).Return([]linodego.LinodeInterface{
					{VPC: &linodego.VPCInterface{VPCID: 123456}},
				}, nil)
				env.client.EXPECT().GetNFSSpace(gomock.Any(), "nfss-123abc").Return(&linodego.NFSSpace{ID: "nfss-123abc"}, nil)
				env.client.EXPECT().
					ListNFSFilesystems(gomock.Any(), "nfss-123abc", gomock.Eq(filesystemOptions)).
					Return([]linodego.NFSFilesystem{{
						ID:          "fs-existing",
						SpaceID:     "nfss-123abc",
						Label:       "pvc-abc",
						Region:      "us-east",
						MountTarget: "nfss-123abc.nfs.us-east.linode.com:/fs-existing",
						Tags:        []string{"tag-a"},
					}}, nil)
			},
			assert: func(t *testing.T, response *csi.CreateVolumeResponse) {
				t.Helper()
				if response.GetVolume().GetVolumeId() != "nfss-123abc/fs-existing" {
					t.Fatalf("unexpected volume id %q", response.GetVolume().GetVolumeId())
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newControllerTestEnv(t)
			if tt.setup != nil {
				tt.setup(t, env)
			}

			response, err := env.server.CreateVolume(context.Background(), tt.request)
			if status.Code(err) != tt.wantCode {
				t.Fatalf("CreateVolume() code = %v, want %v", status.Code(err), tt.wantCode)
			}
			if tt.wantCode != codes.OK {
				return
			}
			if tt.assert != nil {
				tt.assert(t, response)
			}
		})
	}
}

func TestDeleteVolume(t *testing.T) {
	tests := []struct {
		name     string
		request  *csi.DeleteVolumeRequest
		setup    func(controllerTestEnv)
		wantCode codes.Code
	}{
		{
			name:    "not found is success",
			request: &csi.DeleteVolumeRequest{VolumeId: testVolumeID},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().DeleteNFSFilesystem(gomock.Any(), "nfss-123abc", "fs-12345678").Return(linodeAPIError(http.StatusNotFound))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newControllerTestEnv(t)
			if tt.setup != nil {
				tt.setup(env)
			}

			_, err := env.server.DeleteVolume(context.Background(), tt.request)
			if status.Code(err) != tt.wantCode {
				t.Fatalf("DeleteVolume() code = %v, want %v", status.Code(err), tt.wantCode)
			}
		})
	}
}

func TestControllerPublishVolume(t *testing.T) {
	tests := []struct {
		name     string
		request  *csi.ControllerPublishVolumeRequest
		setup    func(controllerTestEnv)
		wantCode codes.Code
	}{
		{
			name: "adds node and enables policy",
			request: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           "202",
				VolumeCapability: mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER),
			},
			setup: func(env controllerTestEnv) {
				policy := &linodego.NFSFilesystemAccessPolicy{
					FilesystemID: "fs-12345678",
					Label:        "policy-a",
					Enabled:      false,
					LinodeIDs:    []int{101},
					RootSquash:   linodego.NFSRootSquashModeNone,
					Protocols:    []linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4},
				}
				env.client.EXPECT().GetNFSFilesystemAccessPolicy(gomock.Any(), "nfss-123abc", "fs-12345678").Return(policy, nil)
				env.client.EXPECT().UpdateNFSFilesystemAccessPolicy(gomock.Any(), "nfss-123abc", "fs-12345678", gomock.Eq(linodego.NFSFilesystemAccessPolicyUpdateOptions{
					Label:      "policy-a",
					Enabled:    boolPtr(true),
					LinodeIDs:  []int{101, 202},
					RootSquash: linodego.NFSRootSquashModeNone,
					Protocols:  []linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4},
				})).Return(policy, nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newControllerTestEnv(t)
			if tt.setup != nil {
				tt.setup(env)
			}

			_, err := env.server.ControllerPublishVolume(context.Background(), tt.request)
			if status.Code(err) != tt.wantCode {
				t.Fatalf("ControllerPublishVolume() code = %v, want %v", status.Code(err), tt.wantCode)
			}
		})
	}
}

func TestControllerUnpublishVolume(t *testing.T) {
	tests := []struct {
		name     string
		request  *csi.ControllerUnpublishVolumeRequest
		setup    func(controllerTestEnv)
		wantCode codes.Code
	}{
		{
			name:    "removes only target node",
			request: &csi.ControllerUnpublishVolumeRequest{VolumeId: testVolumeID, NodeId: "202"},
			setup: func(env controllerTestEnv) {
				policy := &linodego.NFSFilesystemAccessPolicy{
					FilesystemID: "fs-12345678",
					Label:        "policy-a",
					Enabled:      true,
					LinodeIDs:    []int{101, 202, 303},
					RootSquash:   linodego.NFSRootSquashModeRootSquash,
					Protocols:    []linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4},
				}
				env.client.EXPECT().GetNFSFilesystemAccessPolicy(gomock.Any(), "nfss-123abc", "fs-12345678").Return(policy, nil)
				env.client.EXPECT().UpdateNFSFilesystemAccessPolicy(gomock.Any(), "nfss-123abc", "fs-12345678", gomock.Eq(linodego.NFSFilesystemAccessPolicyUpdateOptions{
					Label:      "policy-a",
					Enabled:    boolPtr(true),
					LinodeIDs:  []int{101, 303},
					RootSquash: linodego.NFSRootSquashModeRootSquash,
					Protocols:  []linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4},
				})).Return(policy, nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newControllerTestEnv(t)
			if tt.setup != nil {
				tt.setup(env)
			}

			_, err := env.server.ControllerUnpublishVolume(context.Background(), tt.request)
			if status.Code(err) != tt.wantCode {
				t.Fatalf("ControllerUnpublishVolume() code = %v, want %v", status.Code(err), tt.wantCode)
			}
		})
	}
}

func TestValidateVolumeCapabilities(t *testing.T) {
	tests := []struct {
		name     string
		request  *csi.ValidateVolumeCapabilitiesRequest
		setup    func(controllerTestEnv)
		wantCode codes.Code
		assert   func(t *testing.T, response *csi.ValidateVolumeCapabilitiesResponse)
	}{
		{
			name: "confirms supported mount",
			request: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId:           testVolumeID,
				VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
			},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().GetNFSFilesystem(gomock.Any(), "nfss-123abc", "fs-12345678").Return(&linodego.NFSFilesystem{
					ID:          "fs-12345678",
					SpaceID:     "nfss-123abc",
					Region:      "us-east",
					MountTarget: "nfss-123abc.nfs.us-east.linode.com:/fs-12345678",
				}, nil)
			},
			assert: func(t *testing.T, response *csi.ValidateVolumeCapabilitiesResponse) {
				t.Helper()
				if response.GetConfirmed() == nil {
					t.Fatal("expected confirmed capabilities")
				}
				if _, ok := response.GetConfirmed().GetVolumeContext()[volumeContextOwnedByDriver]; ok {
					t.Fatal("validation response should not mark the volume as dynamically owned")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newControllerTestEnv(t)
			if tt.setup != nil {
				tt.setup(env)
			}

			response, err := env.server.ValidateVolumeCapabilities(context.Background(), tt.request)
			if status.Code(err) != tt.wantCode {
				t.Fatalf("ValidateVolumeCapabilities() code = %v, want %v", status.Code(err), tt.wantCode)
			}
			if tt.wantCode != codes.OK {
				return
			}
			if tt.assert != nil {
				tt.assert(t, response)
			}
		})
	}
}

func TestControllerGetVolume(t *testing.T) {
	tests := []struct {
		name     string
		request  *csi.ControllerGetVolumeRequest
		setup    func(controllerTestEnv)
		wantCode codes.Code
		assert   func(t *testing.T, response *csi.ControllerGetVolumeResponse)
	}{
		{
			name:    "returns filesystem metadata",
			request: &csi.ControllerGetVolumeRequest{VolumeId: testVolumeID},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().GetNFSFilesystem(gomock.Any(), "nfss-123abc", "fs-12345678").Return(&linodego.NFSFilesystem{
					ID:          "fs-12345678",
					SpaceID:     "nfss-123abc",
					Region:      "us-east",
					MountTarget: "nfss-123abc.nfs.us-east.linode.com:/fs-12345678",
				}, nil)
			},
			assert: func(t *testing.T, response *csi.ControllerGetVolumeResponse) {
				t.Helper()
				if response.GetVolume().GetVolumeId() != testVolumeID {
					t.Fatalf("unexpected volume id %q", response.GetVolume().GetVolumeId())
				}
				if response.GetStatus() == nil {
					t.Fatal("expected volume status")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newControllerTestEnv(t)
			if tt.setup != nil {
				tt.setup(env)
			}

			response, err := env.server.ControllerGetVolume(context.Background(), tt.request)
			if status.Code(err) != tt.wantCode {
				t.Fatalf("ControllerGetVolume() code = %v, want %v", status.Code(err), tt.wantCode)
			}
			if tt.wantCode != codes.OK {
				return
			}
			if tt.assert != nil {
				tt.assert(t, response)
			}
		})
	}
}

func TestControllerGetCapabilities(t *testing.T) {
	tests := []struct {
		name     string
		caps     []*csi.ControllerServiceCapability
		wantCaps []*csi.ControllerServiceCapability
	}{
		{
			name: "returns configured capabilities",
			caps: []*csi.ControllerServiceCapability{
				{Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME}}},
			},
			wantCaps: []*csi.ControllerServiceCapability{
				{Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME}}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := &ControllerServer{driver: &LinodeDriver{controllerCaps: tt.caps}}

			response, err := server.ControllerGetCapabilities(context.Background(), &csi.ControllerGetCapabilitiesRequest{})
			if err != nil {
				t.Fatalf("ControllerGetCapabilities() error = %v", err)
			}
			if !reflect.DeepEqual(response.GetCapabilities(), tt.wantCaps) {
				t.Fatalf("ControllerGetCapabilities() = %#v, want %#v", response.GetCapabilities(), tt.wantCaps)
			}
		})
	}
}

func TestControllerServerUnimplementedRPCs(t *testing.T) {
	tests := []struct {
		name string
		call func(*ControllerServer) error
	}{
		{name: "ControllerExpandVolume", call: func(server *ControllerServer) error {
			_, err := server.ControllerExpandVolume(context.Background(), &csi.ControllerExpandVolumeRequest{})
			return err
		}},
		{name: "GetCapacity", call: func(server *ControllerServer) error {
			_, err := server.GetCapacity(context.Background(), &csi.GetCapacityRequest{})
			return err
		}},
		{name: "ListVolumes", call: func(server *ControllerServer) error {
			_, err := server.ListVolumes(context.Background(), &csi.ListVolumesRequest{})
			return err
		}},
		{name: "CreateSnapshot", call: func(server *ControllerServer) error {
			_, err := server.CreateSnapshot(context.Background(), &csi.CreateSnapshotRequest{})
			return err
		}},
		{name: "DeleteSnapshot", call: func(server *ControllerServer) error {
			_, err := server.DeleteSnapshot(context.Background(), &csi.DeleteSnapshotRequest{})
			return err
		}},
		{name: "ListSnapshots", call: func(server *ControllerServer) error {
			_, err := server.ListSnapshots(context.Background(), &csi.ListSnapshotsRequest{})
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := &ControllerServer{driver: &LinodeDriver{}}

			err := tt.call(server)
			if status.Code(err) != codes.Unimplemented {
				t.Fatalf("%s code = %v, want %v", tt.name, status.Code(err), codes.Unimplemented)
			}
		})
	}
}

func mountCapability(mode csi.VolumeCapability_AccessMode_Mode) *csi.VolumeCapability {
	return &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: mode},
	}
}

func linodeAPIError(code int) error {
	return &linodego.Error{Code: code, Message: http.StatusText(code)}
}
