package driver

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/linode/linodego/v2"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	corev1 "k8s.io/api/core/v1"

	"github.com/linode/linode-filestorage-csi-driver/mocks"
	linodeclient "github.com/linode/linode-filestorage-csi-driver/pkg/linode-client"
	"github.com/linode/linode-filestorage-csi-driver/pkg/util"
)

const testVolumeID = "123/456"

var (
	timestamp = new(time.Now())
)

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
	metadataSvc := &metadataService{kubeClient: kubeClient, linodeClient: client}
	return controllerTestEnv{
		client: client,
		kube:   kubeClient,
		server: &ControllerServer{
			driver:      &LinodeDriver{metadata: metadataSvc},
			client:      client,
			volumeLocks: util.NewVolumeLocks(),
		},
	}
}

func TestCreateVolumeSuccessCases(t *testing.T) {
	tests := []struct {
		name    string
		request *csi.CreateVolumeRequest
		setup   func(*testing.T, controllerTestEnv)
		assert  func(*testing.T, *csi.CreateVolumeResponse)
	}{
		{
			name: "creates filesystem",
			request: &csi.CreateVolumeRequest{
				Name:          "pvc-abc",
				CapacityRange: &csi.CapacityRange{RequiredBytes: 1024},
				Parameters: map[string]string{
					storageClassParamSpaceID:    "123",
					storageClassParamTags:       "tag-a, tag-b",
					storageClassParamRootSquash: string(linodego.NFSSquashPolicyRootSquash),
				},
				VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
			},
			setup: func(t *testing.T, env controllerTestEnv) {
				t.Helper()
				filesystemOptions := mustListOptionsForExactFields(t, map[string]string{"label": "pvc-abc", "region": "us-east"})
				expectSingleNodeCluster(env, 123456)
				env.client.EXPECT().GetNFSSpace(gomock.Any(), 123).Return(&linodego.NFSSpace{ID: 123, Label: "prod-space"}, nil)
				env.client.EXPECT().ListNFSFilesystems(gomock.Any(), 123, gomock.Eq(filesystemOptions)).Return(nil, nil)
				expectSpaceVPCAssociation(env, 123, "space-policy", 123456, linodego.NFSMTLSModeRequired)
				env.client.EXPECT().
					CreateNFSFilesystem(gomock.Any(), 123, gomock.Eq(linodego.NFSFilesystemCreateOptions{
						Label:            "pvc-abc",
						Region:           "us-east",
						ProtocolVersions: new([]linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4}),
						Tags:             new([]string{"tag-a", "tag-b"}),
					})).
					Return(&linodego.NFSFilesystem{
						ID:      456,
						SpaceID: 123,
						Label:   "pvc-abc",
						Region:  "us-east",
					}, nil)
				env.client.EXPECT().WaitForNFSFilesystemStatus(gomock.Any(), 123, 456, linodego.NFSFilesystemStatusActive).Return(&linodego.NFSFilesystem{ID: 456, SpaceID: 123, Label: "pvc-abc", Region: "us-east", Status: linodego.NFSFilesystemStatusActive, MountTargetFQDN: new("prod-7b.nfs.us-east.linode.com:/pvc-abc-1c8")}, nil)
				env.client.EXPECT().GetNFSFilesystemAccessPolicy(gomock.Any(), 123, 456).Return(&linodego.NFSFilesystemAccessPolicy{FilesystemID: 456, Enabled: false, SquashPolicy: linodego.NFSSquashPolicyNone}, nil)
				env.client.EXPECT().UpdateNFSFilesystemAccessPolicy(gomock.Any(), 123, 456, gomock.Eq(linodego.NFSFilesystemAccessPolicyUpdateOptions{Label: new(""), Enabled: new(false), LinodeIDs: new([]int(nil)), SquashPolicy: new(linodego.NFSSquashPolicyRootSquash)})).Return(&linodego.NFSFilesystemAccessPolicy{FilesystemID: 456}, nil)
				env.client.EXPECT().WaitForNFSFilesystemAccessPolicyStatus(gomock.Any(), 123, 456, linodego.NFSAccessPolicyStatusActive).Return(&linodego.NFSFilesystemAccessPolicy{FilesystemID: 456, Status: linodego.NFSAccessPolicyStatusActive}, nil)
			},
			assert: func(t *testing.T, response *csi.CreateVolumeResponse) {
				t.Helper()
				assertCreateVolumeResponse(t, response, testVolumeID, 1024, map[string]string{
					volumeContextSpaceID:       "123",
					volumeContextFilesystemID:  "456",
					volumeContextMountTarget:   "prod-7b.nfs.us-east.linode.com:/pvc-abc-1c8",
					volumeContextRegion:        "us-east",
					volumeContextSpaceMTLSMode: string(linodego.NFSMTLSModeRequired),
				})
			},
		},
		{
			name: "lowercases an uppercase volume name for lookup and creation",
			request: &csi.CreateVolumeRequest{
				Name:          "Sanity-TEST-Volume",
				CapacityRange: &csi.CapacityRange{RequiredBytes: 1024},
				Parameters: map[string]string{
					storageClassParamSpaceID: "123",
				},
				VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
			},
			setup: func(t *testing.T, env controllerTestEnv) {
				t.Helper()
				filesystemOptions := mustListOptionsForExactFields(t, map[string]string{"label": "sanity-test-volume", "region": "us-east"})
				expectSingleNodeCluster(env, 123456)
				env.client.EXPECT().GetNFSSpace(gomock.Any(), 123).Return(&linodego.NFSSpace{ID: 123, Label: "prod-space"}, nil)
				env.client.EXPECT().ListNFSFilesystems(gomock.Any(), 123, gomock.Eq(filesystemOptions)).Return(nil, nil)
				expectSpaceVPCAssociation(env, 123, "space-policy", 123456, linodego.NFSMTLSModeRequired)
				env.client.EXPECT().
					CreateNFSFilesystem(gomock.Any(), 123, gomock.Eq(linodego.NFSFilesystemCreateOptions{
						Label:            "sanity-test-volume",
						Region:           "us-east",
						ProtocolVersions: new([]linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4}),
					})).
					Return(&linodego.NFSFilesystem{
						ID:      456,
						SpaceID: 123,
						Label:   "sanity-test-volume",
						Region:  "us-east",
					}, nil)
				env.client.EXPECT().WaitForNFSFilesystemStatus(gomock.Any(), 123, 456, linodego.NFSFilesystemStatusActive).Return(&linodego.NFSFilesystem{ID: 456, SpaceID: 123, Label: "sanity-test-volume", Region: "us-east", Status: linodego.NFSFilesystemStatusActive, MountTargetFQDN: new("prod-7b.nfs.us-east.linode.com:/sanity-test-volume-1c8")}, nil)
			},
			assert: func(t *testing.T, response *csi.CreateVolumeResponse) {
				t.Helper()
				assertCreateVolumeResponse(t, response, testVolumeID, 1024, map[string]string{
					volumeContextSpaceID:       "123",
					volumeContextFilesystemID:  "456",
					volumeContextMountTarget:   "prod-7b.nfs.us-east.linode.com:/sanity-test-volume-1c8",
					volumeContextRegion:        "us-east",
					volumeContextSpaceMTLSMode: string(linodego.NFSMTLSModeRequired),
				})
			},
		},
		{
			name: "lowercases an uppercase name when cloning a snapshot",
			request: &csi.CreateVolumeRequest{
				Name:          "Sanity-TEST-Restore",
				CapacityRange: &csi.CapacityRange{RequiredBytes: 3 * 1024 * 1024 * 1024},
				Parameters: map[string]string{
					storageClassParamSpaceID: "1123",
					storageClassParamTags:    "tag-a",
				},
				VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
				VolumeContentSource: &csi.VolumeContentSource{
					Type: &csi.VolumeContentSource_Snapshot{
						Snapshot: &csi.VolumeContentSource_SnapshotSource{
							SnapshotId: "123/4567/890",
						},
					},
				},
			},
			setup: func(t *testing.T, env controllerTestEnv) {
				t.Helper()
				filesystemOptions := mustListOptionsForExactFields(t, map[string]string{"label": "sanity-test-restore", "region": "us-east"})
				expectSingleNodeCluster(env, 123456)
				env.client.EXPECT().GetNFSSpace(gomock.Any(), 1123).Return(&linodego.NFSSpace{ID: 1123}, nil)
				env.client.EXPECT().ListNFSFilesystems(gomock.Any(), 1123, gomock.Eq(filesystemOptions)).Return(nil, nil)
				expectSpaceVPCAssociation(env, 1123, "space-policy", 123456, linodego.NFSMTLSModeOptional)
				env.client.EXPECT().CloneNFSSnapshot(gomock.Any(), 123, 4567, 890, linodego.NFSSnapshotCloneOptions{
					Label:   "sanity-test-restore",
					Region:  "us-east",
					SpaceID: new(new(1123)),
					Tags:    new([]string{"tag-a"}),
				}).Return(&linodego.NFSFilesystem{
					ID:               890,
					SourceSnapshotID: new(890),
					SpaceID:          1123,
					Label:            "sanity-test-restore",
					Region:           "us-east",
					Status:           linodego.NFSFilesystemStatusCreating,
				}, nil)
				env.client.EXPECT().WaitForNFSFilesystemStatus(gomock.Any(), 1123, 890, linodego.NFSFilesystemStatusActive).Return(&linodego.NFSFilesystem{
					ID:               890,
					SourceSnapshotID: new(890),
					SpaceID:          1123,
					MountTargetFQDN:  new("prod-7b.nfs.us-east.linode.com:/sanity-test-restore-315"),
					Label:            "sanity-test-restore",
					Region:           "us-east",
					Status:           linodego.NFSFilesystemStatusActive,
					Tags:             []string{"tag-a"},
				}, nil)
			},
			assert: func(t *testing.T, response *csi.CreateVolumeResponse) {
				t.Helper()
				assertCreateVolumeResponse(t, response, "1123/890", 3*1024*1024*1024, map[string]string{
					volumeContextSpaceID:       "1123",
					volumeContextFilesystemID:  "890",
					volumeContextMountTarget:   "prod-7b.nfs.us-east.linode.com:/sanity-test-restore-315",
					volumeContextRegion:        "us-east",
					volumeContextSpaceMTLSMode: string(linodego.NFSMTLSModeOptional),
				})
			},
		},
		{
			name: "returns existing compatible filesystem",
			request: &csi.CreateVolumeRequest{
				Name:               "pvc-abc",
				Parameters:         map[string]string{storageClassParamSpaceID: "123", storageClassParamTags: "tag-a"},
				VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
			},
			setup: func(t *testing.T, env controllerTestEnv) {
				t.Helper()
				filesystemOptions := mustListOptionsForExactFields(t, map[string]string{"label": "pvc-abc", "region": "us-east"})
				expectSingleNodeCluster(env, 123456)
				gomock.InOrder(
					env.client.EXPECT().GetNFSSpace(gomock.Any(), 123).Return(&linodego.NFSSpace{ID: 123}, nil),
					env.client.EXPECT().ListNFSFilesystems(gomock.Any(), 123, gomock.Eq(filesystemOptions)).Return([]linodego.NFSFilesystem{{
						ID:              789,
						SpaceID:         123,
						Label:           "pvc-abc",
						Region:          "us-east",
						Status:          linodego.NFSFilesystemStatusActive,
						MountTargetFQDN: new("prod-7b.nfs.us-east.linode.com:/pvc-abc-315"),
						Tags:            []string{"tag-a"},
					}}, nil),
					env.client.EXPECT().WaitForNFSFilesystemStatus(gomock.Any(), 123, 789, linodego.NFSFilesystemStatusActive).Return(&linodego.NFSFilesystem{
						ID:              789,
						SpaceID:         123,
						Label:           "pvc-abc",
						Region:          "us-east",
						Status:          linodego.NFSFilesystemStatusActive,
						MountTargetFQDN: new("prod-7b.nfs.us-east.linode.com:/pvc-abc-315"),
						Tags:            []string{"tag-a"},
					}, nil),
					env.client.EXPECT().GetNFSSpaceAccessPolicy(gomock.Any(), 123).Return(&linodego.NFSSpaceAccessPolicy{MTLSMode: linodego.NFSMTLSModeOptional}, nil),
					env.client.EXPECT().UpdateNFSSpaceAccessPolicy(gomock.Any(), 123, gomock.Eq(linodego.NFSSpaceAccessPolicyUpdateOptions{
						Label:    new(""),
						Enabled:  new(true),
						VPCs:     new([]linodego.NFSSpaceAccessPolicyVPCOptions{{ID: 123456}}),
						MTLSMode: new(linodego.NFSMTLSModeOptional),
					})).Return(&linodego.NFSSpaceAccessPolicy{}, nil),
					env.client.EXPECT().WaitForNFSSpaceAccessPolicyStatus(gomock.Any(), 123, linodego.NFSAccessPolicyStatusActive).Return(&linodego.NFSSpaceAccessPolicy{}, nil),
				)
			},
			assert: func(t *testing.T, response *csi.CreateVolumeResponse) {
				t.Helper()
				assertCreateVolumeResponse(t, response, "123/789", 0, map[string]string{
					volumeContextSpaceID:       "123",
					volumeContextFilesystemID:  "789",
					volumeContextMountTarget:   "prod-7b.nfs.us-east.linode.com:/pvc-abc-315",
					volumeContextRegion:        "us-east",
					volumeContextSpaceMTLSMode: string(linodego.NFSMTLSModeOptional),
				})
			},
		},
		{
			name: "resolves space by label",
			request: &csi.CreateVolumeRequest{
				Name: "pvc-abc",
				Parameters: map[string]string{
					storageClassParamSpaceLabel: "prod-space",
					storageClassParamTags:       "tag-a",
				},
				VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
			},
			setup: func(t *testing.T, env controllerTestEnv) {
				t.Helper()
				spaceOptions := mustListOptionsForExactFields(t, map[string]string{"label": "prod-space"})
				filesystemOptions := mustListOptionsForExactFields(t, map[string]string{"label": "pvc-abc", "region": "us-east"})
				expectSingleNodeCluster(env, 123456)
				env.client.EXPECT().ListNFSSpaces(gomock.Any(), gomock.Eq(spaceOptions)).Return([]linodego.NFSSpace{{ID: 123, Label: "prod-space"}}, nil)
				env.client.EXPECT().ListNFSFilesystems(gomock.Any(), 123, gomock.Eq(filesystemOptions)).Return(nil, nil)
				expectSpaceVPCAssociation(env, 123, "space-policy", 123456, linodego.NFSMTLSModeDisabled)
				env.client.EXPECT().CreateNFSFilesystem(gomock.Any(), 123, gomock.Eq(linodego.NFSFilesystemCreateOptions{Label: "pvc-abc", Region: "us-east", ProtocolVersions: new([]linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4}), Tags: new([]string{"tag-a"})})).Return(&linodego.NFSFilesystem{ID: 456, SpaceID: 123, Label: "pvc-abc", Region: "us-east"}, nil)
				env.client.EXPECT().WaitForNFSFilesystemStatus(gomock.Any(), 123, 456, linodego.NFSFilesystemStatusActive).Return(&linodego.NFSFilesystem{ID: 456, SpaceID: 123, Label: "pvc-abc", Region: "us-east", Status: linodego.NFSFilesystemStatusActive, MountTargetFQDN: new("prod-7b.nfs.us-east.linode.com:/pvc-abc-1c8")}, nil)
			},
			assert: func(t *testing.T, response *csi.CreateVolumeResponse) {
				t.Helper()
				assertCreateVolumeResponse(t, response, testVolumeID, 0, map[string]string{
					volumeContextSpaceID:       "123",
					volumeContextFilesystemID:  "456",
					volumeContextMountTarget:   "prod-7b.nfs.us-east.linode.com:/pvc-abc-1c8",
					volumeContextRegion:        "us-east",
					volumeContextSpaceMTLSMode: string(linodego.NFSMTLSModeDisabled),
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runCreateVolumeTest(t, tt.request, tt.setup, codes.OK, tt.assert, "")
		})
	}
}

func TestCreateVolumeFailureCases(t *testing.T) {
	tests := []struct {
		name        string
		request     *csi.CreateVolumeRequest
		setup       func(*testing.T, controllerTestEnv)
		wantCode    codes.Code
		wantMessage string
	}{
		{
			name: "fails when created filesystem lacks mount target",
			request: &csi.CreateVolumeRequest{
				Name:               "pvc-abc",
				Parameters:         map[string]string{storageClassParamSpaceID: "123"},
				VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
			},
			setup: func(t *testing.T, env controllerTestEnv) {
				t.Helper()
				filesystemOptions := mustListOptionsForExactFields(t, map[string]string{"label": "pvc-abc", "region": "us-east"})
				expectSingleNodeCluster(env, 123456)
				env.client.EXPECT().GetNFSSpace(gomock.Any(), 123).Return(&linodego.NFSSpace{ID: 123, Label: "prod-space"}, nil)
				env.client.EXPECT().ListNFSFilesystems(gomock.Any(), 123, gomock.Eq(filesystemOptions)).Return(nil, nil)
				expectSpaceVPCAssociation(env, 123, "space-policy", 123456, linodego.NFSMTLSModeRequired)
				env.client.EXPECT().CreateNFSFilesystem(gomock.Any(), 123, gomock.Any()).Return(&linodego.NFSFilesystem{ID: 456, SpaceID: 123, Label: "pvc-abc", Region: "us-east"}, nil)
				env.client.EXPECT().WaitForNFSFilesystemStatus(gomock.Any(), 123, 456, linodego.NFSFilesystemStatusActive).Return(&linodego.NFSFilesystem{ID: 456, SpaceID: 123, Label: "pvc-abc", Region: "us-east", Status: linodego.NFSFilesystemStatusActive}, nil)
			},
			wantCode:    codes.FailedPrecondition,
			wantMessage: "NFS filesystem 456 does not have a mount target",
		},
		{
			name: "fails when existing filesystem lacks mount target",
			request: &csi.CreateVolumeRequest{
				Name:               "pvc-abc",
				Parameters:         map[string]string{storageClassParamSpaceID: "123"},
				VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
			},
			setup: func(t *testing.T, env controllerTestEnv) {
				t.Helper()
				filesystemOptions := mustListOptionsForExactFields(t, map[string]string{"label": "pvc-abc", "region": "us-east"})
				expectSingleNodeCluster(env, 123456)
				env.client.EXPECT().GetNFSSpace(gomock.Any(), 123).Return(&linodego.NFSSpace{ID: 123, Label: "prod-space"}, nil)
				env.client.EXPECT().ListNFSFilesystems(gomock.Any(), 123, gomock.Eq(filesystemOptions)).Return([]linodego.NFSFilesystem{{ID: 456, SpaceID: 123, Label: "pvc-abc", Region: "us-east"}}, nil)
				env.client.EXPECT().WaitForNFSFilesystemStatus(gomock.Any(), 123, 456, linodego.NFSFilesystemStatusActive).Return(&linodego.NFSFilesystem{ID: 456, SpaceID: 123, Label: "pvc-abc", Region: "us-east", Status: linodego.NFSFilesystemStatusActive}, nil)
			},
			wantCode:    codes.FailedPrecondition,
			wantMessage: "NFS filesystem 456 does not have a mount target",
		},
		{
			name: "fails when label matches multiple spaces",
			request: &csi.CreateVolumeRequest{
				Name:               "pvc-abc",
				Parameters:         map[string]string{storageClassParamSpaceLabel: "prod-space"},
				VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
			},
			setup: func(t *testing.T, env controllerTestEnv) {
				t.Helper()
				spaceOptions := mustListOptionsForExactFields(t, map[string]string{"label": "prod-space"})
				expectSingleNodeCluster(env, 123456)
				env.client.EXPECT().ListNFSSpaces(gomock.Any(), gomock.Eq(spaceOptions)).Return([]linodego.NFSSpace{{ID: 123}, {ID: 456}}, nil)
			},
			wantCode: codes.FailedPrecondition,
		},
		{
			name: "fails when cluster VPC is unavailable",
			request: &csi.CreateVolumeRequest{
				Name:               "pvc-abc",
				Parameters:         map[string]string{storageClassParamSpaceID: "123"},
				VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
			},
			setup: func(t *testing.T, env controllerTestEnv) {
				t.Helper()
				env.kube.EXPECT().ListNodes(gomock.Any()).Return(&corev1.NodeList{Items: []corev1.Node{
					*newTestNode("worker-a", "us-east", "10.0.0.20", "linode://202"),
				}}, nil)
				env.client.EXPECT().GetInstance(gomock.Any(), 202).Return(&linodego.Instance{InterfaceGeneration: linodego.GenerationLinode}, nil)
				env.client.EXPECT().ListInterfaces(gomock.Any(), 202, gomock.Nil()).Return(nil, nil)
			},
			wantCode:    codes.FailedPrecondition,
			wantMessage: "this driver requires VPC-backed IPv6 connectivity; cluster VPC not found",
		},
		{
			name: "returns not found for an opaque snapshot handle",
			request: &csi.CreateVolumeRequest{
				Name:          "pvc-abc",
				CapacityRange: &csi.CapacityRange{RequiredBytes: 3 * 1024 * 1024 * 1024},
				Parameters: map[string]string{
					storageClassParamSpaceID: "123",
					storageClassParamTags:    "tag-a",
				},
				VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
				VolumeContentSource: &csi.VolumeContentSource{
					Type: &csi.VolumeContentSource_Snapshot{
						Snapshot: &csi.VolumeContentSource_SnapshotSource{
							SnapshotId: "foobar",
						},
					},
				},
			},
			wantCode:    codes.NotFound,
			wantMessage: "snapshot id \"foobar\" was not found",
		},
		{
			name: "Gateway timeout while cloning",
			request: &csi.CreateVolumeRequest{
				Name:          "pvc-abc",
				CapacityRange: &csi.CapacityRange{RequiredBytes: 3 * 1024 * 1024 * 1024},
				Parameters: map[string]string{
					storageClassParamSpaceID: "123",
					storageClassParamTags:    "tag-a",
				},
				VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
				VolumeContentSource: &csi.VolumeContentSource{
					Type: &csi.VolumeContentSource_Snapshot{
						Snapshot: &csi.VolumeContentSource_SnapshotSource{
							SnapshotId: "123/4567/890",
						},
					},
				},
			},
			setup: func(t *testing.T, env controllerTestEnv) {
				t.Helper()
				filesystemOptions := mustListOptionsForExactFields(t, map[string]string{"label": "pvc-abc", "region": "us-east"})
				expectSingleNodeCluster(env, 123456)
				env.client.EXPECT().GetNFSSpace(gomock.Any(), 123).Return(&linodego.NFSSpace{ID: 123}, nil)
				env.client.EXPECT().ListNFSFilesystems(gomock.Any(), 123, gomock.Eq(filesystemOptions)).Return(nil, nil)
				expectSpaceVPCAssociation(env, 123, "space-policy", 123456, linodego.NFSMTLSModeOptional)
				env.client.EXPECT().CloneNFSSnapshot(gomock.Any(), 123, 4567, 890, linodego.NFSSnapshotCloneOptions{
					Label:   "pvc-abc",
					Region:  "us-east",
					SpaceID: new(new(123)),
					Tags:    new([]string{"tag-a"}),
				}).Return(nil, &linodego.Error{Code: http.StatusGatewayTimeout, Message: "unavailable"})
			},
			wantCode: codes.Unavailable,
		},
		{
			name: "fails while waiting for cloned filesystem",
			request: &csi.CreateVolumeRequest{
				Name:          "pvc-abc",
				CapacityRange: &csi.CapacityRange{RequiredBytes: 3 * 1024 * 1024 * 1024},
				Parameters: map[string]string{
					storageClassParamSpaceID: "123",
					storageClassParamTags:    "tag-a",
				},
				VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
				VolumeContentSource: &csi.VolumeContentSource{
					Type: &csi.VolumeContentSource_Snapshot{
						Snapshot: &csi.VolumeContentSource_SnapshotSource{
							SnapshotId: "123/4567/890",
						},
					},
				},
			},
			setup: func(t *testing.T, env controllerTestEnv) {
				t.Helper()
				filesystemOptions := mustListOptionsForExactFields(t, map[string]string{"label": "pvc-abc", "region": "us-east"})
				expectSingleNodeCluster(env, 123456)
				env.client.EXPECT().GetNFSSpace(gomock.Any(), 123).Return(&linodego.NFSSpace{ID: 123}, nil)
				env.client.EXPECT().ListNFSFilesystems(gomock.Any(), 123, gomock.Eq(filesystemOptions)).Return(nil, nil)
				expectSpaceVPCAssociation(env, 123, "space-policy", 123456, linodego.NFSMTLSModeOptional)
				env.client.EXPECT().CloneNFSSnapshot(gomock.Any(), 123, 4567, 890, linodego.NFSSnapshotCloneOptions{
					Label:   "pvc-abc",
					Region:  "us-east",
					SpaceID: new(new(123)),
					Tags:    new([]string{"tag-a"}),
				}).Return(&linodego.NFSFilesystem{ID: 890, SpaceID: 123, Label: "pvc-abc", Region: "us-east"}, nil)
				env.client.EXPECT().WaitForNFSFilesystemStatus(gomock.Any(), 123, 890, linodego.NFSFilesystemStatusActive).Return(nil, &linodego.Error{Code: http.StatusGatewayTimeout})
			},
			wantCode: codes.Unavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runCreateVolumeTest(t, tt.request, tt.setup, tt.wantCode, nil, tt.wantMessage)
		})
	}
}

func TestCreateVolumeSnapshotCloneContracts(t *testing.T) {
	newRequest := func() *csi.CreateVolumeRequest {
		return &csi.CreateVolumeRequest{
			Name:          "pvc-abc",
			CapacityRange: &csi.CapacityRange{RequiredBytes: 3 * 1024 * 1024 * 1024},
			Parameters: map[string]string{
				storageClassParamSpaceID: "1123",
				storageClassParamTags:    "tag-a",
			},
			VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
			VolumeContentSource: &csi.VolumeContentSource{
				Type: &csi.VolumeContentSource_Snapshot{
					Snapshot: &csi.VolumeContentSource_SnapshotSource{SnapshotId: "123/4567/890"},
				},
			},
		}
	}

	t.Run("retries an existing compatible clone", func(t *testing.T) {
		filesystemOptions := mustListOptionsForExactFields(t, map[string]string{"label": "pvc-abc", "region": "us-east"})
		runCreateVolumeTest(t, newRequest(), func(t *testing.T, env controllerTestEnv) {
			t.Helper()
			expectSingleNodeCluster(env, 123456)
			env.client.EXPECT().GetNFSSpace(gomock.Any(), 1123).Return(&linodego.NFSSpace{ID: 1123}, nil)
			env.client.EXPECT().ListNFSFilesystems(gomock.Any(), 1123, gomock.Eq(filesystemOptions)).Return([]linodego.NFSFilesystem{{
				ID:               890,
				SpaceID:          1123,
				Label:            "pvc-abc",
				Region:           "us-east",
				SourceSnapshotID: new(890),
				Tags:             []string{"tag-a"},
			}}, nil)
			env.client.EXPECT().WaitForNFSFilesystemStatus(gomock.Any(), 1123, 890, linodego.NFSFilesystemStatusActive).Return(&linodego.NFSFilesystem{
				ID:               890,
				SpaceID:          1123,
				Label:            "pvc-abc",
				Region:           "us-east",
				SourceSnapshotID: new(890),
				Tags:             []string{"tag-a"},
				MountTargetFQDN:  new("prod-7b.nfs.us-east.linode.com:/pvc-abc-315"),
			}, nil)
			env.client.EXPECT().GetNFSSpaceAccessPolicy(gomock.Any(), 1123).Return(&linodego.NFSSpaceAccessPolicy{MTLSMode: linodego.NFSMTLSModeOptional}, nil)
			env.client.EXPECT().UpdateNFSSpaceAccessPolicy(gomock.Any(), 1123, gomock.Eq(linodego.NFSSpaceAccessPolicyUpdateOptions{
				Label:    new(""),
				Enabled:  new(true),
				VPCs:     new([]linodego.NFSSpaceAccessPolicyVPCOptions{{ID: 123456}}),
				MTLSMode: new(linodego.NFSMTLSModeOptional),
			})).Return(&linodego.NFSSpaceAccessPolicy{}, nil)
			env.client.EXPECT().WaitForNFSSpaceAccessPolicyStatus(gomock.Any(), 1123, linodego.NFSAccessPolicyStatusActive).Return(&linodego.NFSSpaceAccessPolicy{}, nil)
		}, codes.OK, func(t *testing.T, response *csi.CreateVolumeResponse) {
			t.Helper()
			assertCreateVolumeResponse(t, response, "1123/890", 3*1024*1024*1024, map[string]string{
				volumeContextSpaceID:       "1123",
				volumeContextFilesystemID:  "890",
				volumeContextMountTarget:   "prod-7b.nfs.us-east.linode.com:/pvc-abc-315",
				volumeContextRegion:        "us-east",
				volumeContextSpaceMTLSMode: string(linodego.NFSMTLSModeOptional),
			})
		}, "")
	})

	t.Run("rejects an existing filesystem from another snapshot", func(t *testing.T) {
		filesystemOptions := mustListOptionsForExactFields(t, map[string]string{"label": "pvc-abc", "region": "us-east"})
		runCreateVolumeTest(t, newRequest(), func(t *testing.T, env controllerTestEnv) {
			t.Helper()
			expectSingleNodeCluster(env, 123456)
			env.client.EXPECT().GetNFSSpace(gomock.Any(), 1123).Return(&linodego.NFSSpace{ID: 1123}, nil)
			env.client.EXPECT().ListNFSFilesystems(gomock.Any(), 1123, gomock.Eq(filesystemOptions)).Return([]linodego.NFSFilesystem{{
				ID:               891,
				SpaceID:          1123,
				Label:            "pvc-abc",
				Region:           "us-east",
				SourceSnapshotID: new(889),
				Tags:             []string{"tag-a"},
			}}, nil)
		}, codes.AlreadyExists, nil, "")
	})

	t.Run("rejects an existing filesystem without a source snapshot", func(t *testing.T) {
		filesystemOptions := mustListOptionsForExactFields(t, map[string]string{"label": "pvc-abc", "region": "us-east"})
		runCreateVolumeTest(t, newRequest(), func(t *testing.T, env controllerTestEnv) {
			t.Helper()
			expectSingleNodeCluster(env, 123456)
			env.client.EXPECT().GetNFSSpace(gomock.Any(), 1123).Return(&linodego.NFSSpace{ID: 1123}, nil)
			env.client.EXPECT().ListNFSFilesystems(gomock.Any(), 1123, gomock.Eq(filesystemOptions)).Return([]linodego.NFSFilesystem{{
				ID:      891,
				SpaceID: 1123,
				Label:   "pvc-abc",
				Region:  "us-east",
			}}, nil)
		}, codes.AlreadyExists, nil, `NFS filesystem 891 ("pvc-abc") does not identify a source snapshot; requested snapshot 890`)
	})

	t.Run("rejects an existing clone with incompatible tags", func(t *testing.T) {
		filesystemOptions := mustListOptionsForExactFields(t, map[string]string{"label": "pvc-abc", "region": "us-east"})
		runCreateVolumeTest(t, newRequest(), func(t *testing.T, env controllerTestEnv) {
			t.Helper()
			expectSingleNodeCluster(env, 123456)
			env.client.EXPECT().GetNFSSpace(gomock.Any(), 1123).Return(&linodego.NFSSpace{ID: 1123}, nil)
			env.client.EXPECT().ListNFSFilesystems(gomock.Any(), 1123, gomock.Eq(filesystemOptions)).Return([]linodego.NFSFilesystem{{
				ID:               890,
				SpaceID:          1123,
				Label:            "pvc-abc",
				Region:           "us-east",
				SourceSnapshotID: new(890),
				Tags:             []string{"tag-b"},
			}}, nil)
			env.client.EXPECT().WaitForNFSFilesystemStatus(gomock.Any(), 1123, 890, linodego.NFSFilesystemStatusActive).Return(&linodego.NFSFilesystem{
				ID:               890,
				SpaceID:          1123,
				Label:            "pvc-abc",
				Region:           "us-east",
				SourceSnapshotID: new(890),
				Tags:             []string{"tag-b"},
				MountTargetFQDN:  new("prod-7b.nfs.us-east.linode.com:/pvc-abc-315"),
			}, nil)
		}, codes.AlreadyExists, nil, "")
	})

	t.Run("rejects an active clone without a mount target", func(t *testing.T) {
		filesystemOptions := mustListOptionsForExactFields(t, map[string]string{"label": "pvc-abc", "region": "us-east"})
		runCreateVolumeTest(t, newRequest(), func(t *testing.T, env controllerTestEnv) {
			t.Helper()
			expectSingleNodeCluster(env, 123456)
			env.client.EXPECT().GetNFSSpace(gomock.Any(), 1123).Return(&linodego.NFSSpace{ID: 1123}, nil)
			env.client.EXPECT().ListNFSFilesystems(gomock.Any(), 1123, gomock.Eq(filesystemOptions)).Return(nil, nil)
			expectSpaceVPCAssociation(env, 1123, "space-policy", 123456, linodego.NFSMTLSModeOptional)
			env.client.EXPECT().CloneNFSSnapshot(gomock.Any(), 123, 4567, 890, linodego.NFSSnapshotCloneOptions{
				Label:   "pvc-abc",
				Region:  "us-east",
				SpaceID: new(new(1123)),
				Tags:    new([]string{"tag-a"}),
			}).Return(&linodego.NFSFilesystem{ID: 890, SpaceID: 1123, Label: "pvc-abc", Region: "us-east"}, nil)
			env.client.EXPECT().WaitForNFSFilesystemStatus(gomock.Any(), 1123, 890, linodego.NFSFilesystemStatusActive).Return(&linodego.NFSFilesystem{
				ID: 890, SpaceID: 1123, Label: "pvc-abc", Region: "us-east", Status: linodego.NFSFilesystemStatusActive,
			}, nil)
		}, codes.FailedPrecondition, nil, "NFS filesystem 890 does not have a mount target")
	})

	t.Run("rejects unsupported volume source before provisioning", func(t *testing.T) {
		request := newRequest()
		request.VolumeContentSource = &csi.VolumeContentSource{
			Type: &csi.VolumeContentSource_Volume{
				Volume: &csi.VolumeContentSource_VolumeSource{VolumeId: "123/456"},
			},
		}
		runCreateVolumeTest(t, request, nil, codes.InvalidArgument, nil, "unsupported volume content source")
	})
}

func runCreateVolumeTest(t *testing.T, request *csi.CreateVolumeRequest, setup func(*testing.T, controllerTestEnv), wantCode codes.Code, assert func(*testing.T, *csi.CreateVolumeResponse), wantMessage string) {
	t.Helper()

	env := newControllerTestEnv(t)
	if setup != nil {
		setup(t, env)
	}

	response, err := env.server.CreateVolume(context.Background(), request)
	if status.Code(err) != wantCode {
		t.Fatalf("CreateVolume() code = %v, want %v", status.Code(err), wantCode)
	}
	if wantCode != codes.OK {
		if wantMessage != "" {
			if got := status.Convert(err).Message(); got != wantMessage {
				t.Fatalf("CreateVolume() message = %q, want %q", got, wantMessage)
			}
		}
		return
	}
	if assert != nil {
		assert(t, response)
	}
}

func mustListOptionsForExactFields(t *testing.T, fields map[string]string) *linodego.ListOptions {
	t.Helper()

	options, err := listOptionsForExactFields(fields)
	if err != nil {
		t.Fatalf("list options: %v", err)
	}
	return options
}

func expectSingleNodeCluster(env controllerTestEnv, vpcID int) {
	env.kube.EXPECT().ListNodes(gomock.Any()).Return(&corev1.NodeList{Items: []corev1.Node{
		*newTestNode("worker-a", "us-east", "10.0.0.20", "linode://202"),
	}}, nil)
	env.client.EXPECT().GetInstance(gomock.Any(), 202).Return(&linodego.Instance{InterfaceGeneration: linodego.GenerationLinode}, nil)
	env.client.EXPECT().ListInterfaces(gomock.Any(), 202, gomock.Nil()).Return([]linodego.LinodeInterface{{
		VPC: &linodego.VPCInterface{VPCID: vpcID},
	}}, nil)
}

func expectSpaceVPCAssociation(env controllerTestEnv, spaceID int, label string, vpcID int, mtlsMode linodego.NFSMTLSMode) {
	env.client.EXPECT().GetNFSSpaceAccessPolicy(gomock.Any(), spaceID).Return(&linodego.NFSSpaceAccessPolicy{
		Label:    label,
		Enabled:  true,
		MTLSMode: mtlsMode,
	}, nil)
	env.client.EXPECT().UpdateNFSSpaceAccessPolicy(gomock.Any(), spaceID, gomock.Eq(linodego.NFSSpaceAccessPolicyUpdateOptions{
		Label:    new(label),
		Enabled:  new(true),
		VPCs:     new([]linodego.NFSSpaceAccessPolicyVPCOptions{{ID: vpcID}}),
		MTLSMode: new(mtlsMode),
	})).Return(&linodego.NFSSpaceAccessPolicy{VPCACL: []linodego.NFSSpaceAccessPolicyVPC{{ID: vpcID}}, MTLSMode: mtlsMode}, nil)
	env.client.EXPECT().WaitForNFSSpaceAccessPolicyStatus(gomock.Any(), spaceID, linodego.NFSAccessPolicyStatusActive).Return(&linodego.NFSSpaceAccessPolicy{VPCACL: []linodego.NFSSpaceAccessPolicyVPC{{ID: vpcID}}, MTLSMode: mtlsMode, Status: linodego.NFSAccessPolicyStatusActive}, nil)
}

func assertCreateVolumeResponse(t *testing.T, response *csi.CreateVolumeResponse, wantVolumeID string, wantCapacity int64, wantContext map[string]string) {
	t.Helper()

	volume := response.GetVolume()
	if volume.GetVolumeId() != wantVolumeID {
		t.Fatalf("unexpected volume id %q", volume.GetVolumeId())
	}
	if volume.GetCapacityBytes() != wantCapacity {
		t.Fatalf("unexpected capacity %d", volume.GetCapacityBytes())
	}
	if !reflect.DeepEqual(volume.GetVolumeContext(), wantContext) {
		t.Fatalf("unexpected volume context %#v", volume.GetVolumeContext())
	}
}

func testLinodeACL(ids ...int) []linodego.NFSFilesystemAccessPolicyLinode {
	acl := make([]linodego.NFSFilesystemAccessPolicyLinode, 0, len(ids))
	for _, id := range ids {
		acl = append(acl, linodego.NFSFilesystemAccessPolicyLinode{ID: id})
	}
	return acl
}

func testFilesystemPolicyUpdate(label string, enabled bool, linodeIDs []int, squashPolicy linodego.NFSSquashPolicy) linodego.NFSFilesystemAccessPolicyUpdateOptions {
	return linodego.NFSFilesystemAccessPolicyUpdateOptions{
		Label:        new(label),
		Enabled:      new(enabled),
		LinodeIDs:    new(linodeIDs),
		SquashPolicy: new(squashPolicy),
		Protocols:    new([]linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4}),
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
			name:    "malformed non-empty volume id is success",
			request: &csi.DeleteVolumeRequest{VolumeId: "not-a-volume-handle"},
		},
		{
			name:     "empty volume id",
			request:  &csi.DeleteVolumeRequest{},
			wantCode: codes.InvalidArgument,
		},
		{
			name:    "whitespace volume id is success",
			request: &csi.DeleteVolumeRequest{VolumeId: " \t "},
		},
		{
			name:    "not found is success",
			request: &csi.DeleteVolumeRequest{VolumeId: testVolumeID},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().DeleteNFSFilesystem(gomock.Any(), 123, 456).Return(linodeAPIError(http.StatusNotFound))
			},
		},
		{
			name:     "non-not-found error is preserved",
			request:  &csi.DeleteVolumeRequest{VolumeId: testVolumeID},
			wantCode: codes.FailedPrecondition,
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().DeleteNFSFilesystem(gomock.Any(), 123, 456).Return(linodeAPIError(http.StatusConflict))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newControllerTestEnv(t)
			if tt.setup != nil {
				tt.setup(env)
			}

			response, err := env.server.DeleteVolume(context.Background(), tt.request)
			if status.Code(err) != tt.wantCode {
				t.Fatalf("DeleteVolume() code = %v, want %v (err = %v)", status.Code(err), tt.wantCode, err)
			}
			if tt.wantCode != codes.OK {
				return
			}
			if !reflect.DeepEqual(response, &csi.DeleteVolumeResponse{}) {
				t.Fatalf("DeleteVolume() response = %#v, want empty response", response)
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
					FilesystemID: 456,
					Label:        "policy-a",
					Enabled:      false,
					LinodeACL:    testLinodeACL(101),
					SquashPolicy: linodego.NFSSquashPolicyNone,
					Protocols:    []linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4},
				}
				env.client.EXPECT().GetNFSFilesystemAccessPolicy(gomock.Any(), 123, 456).Return(policy, nil)
				env.client.EXPECT().UpdateNFSFilesystemAccessPolicy(gomock.Any(), 123, 456, gomock.Eq(testFilesystemPolicyUpdate("policy-a", true, []int{101, 202}, linodego.NFSSquashPolicyNone))).Return(policy, nil)
				env.client.EXPECT().WaitForNFSFilesystemAccessPolicyStatus(gomock.Any(), 123, 456, linodego.NFSAccessPolicyStatusActive).Return(policy, nil)
			},
		},
		{
			name: "is idempotent when node already allowed",
			request: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           "202",
				VolumeCapability: mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER),
			},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().GetNFSFilesystemAccessPolicy(gomock.Any(), 123, 456).Return(&linodego.NFSFilesystemAccessPolicy{FilesystemID: 456, Enabled: true, LinodeACL: testLinodeACL(101, 202)}, nil)
			},
		},
		{
			name: "enables policy without duplicating existing node",
			request: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           "202",
				VolumeCapability: mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER),
			},
			setup: func(env controllerTestEnv) {
				policy := &linodego.NFSFilesystemAccessPolicy{
					FilesystemID: 456,
					Label:        "policy-a",
					Enabled:      false,
					LinodeACL:    testLinodeACL(101, 202),
					SquashPolicy: linodego.NFSSquashPolicyNone,
					Protocols:    []linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4},
				}
				env.client.EXPECT().GetNFSFilesystemAccessPolicy(gomock.Any(), 123, 456).Return(policy, nil)
				env.client.EXPECT().UpdateNFSFilesystemAccessPolicy(gomock.Any(), 123, 456, gomock.Eq(testFilesystemPolicyUpdate("policy-a", true, []int{101, 202}, linodego.NFSSquashPolicyNone))).Return(policy, nil)
				env.client.EXPECT().WaitForNFSFilesystemAccessPolicyStatus(gomock.Any(), 123, 456, linodego.NFSAccessPolicyStatusActive).Return(policy, nil)
			},
		},
		{
			name: "preserves existing policy fields",
			request: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           "202",
				VolumeCapability: mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER),
			},
			setup: func(env controllerTestEnv) {
				policy := &linodego.NFSFilesystemAccessPolicy{
					FilesystemID: 456,
					Label:        "policy-a",
					Enabled:      true,
					LinodeACL:    testLinodeACL(101),
					SquashPolicy: linodego.NFSSquashPolicyRootSquash,
					Protocols:    []linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4},
				}
				env.client.EXPECT().GetNFSFilesystemAccessPolicy(gomock.Any(), 123, 456).Return(policy, nil)
				env.client.EXPECT().UpdateNFSFilesystemAccessPolicy(gomock.Any(), 123, 456, gomock.Eq(testFilesystemPolicyUpdate("policy-a", true, []int{101, 202}, linodego.NFSSquashPolicyRootSquash))).Return(policy, nil)
				env.client.EXPECT().WaitForNFSFilesystemAccessPolicyStatus(gomock.Any(), 123, 456, linodego.NFSAccessPolicyStatusActive).Return(policy, nil)
			},
		},
		{
			name: "rejects malformed node id",
			request: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				NodeId:           "node-a",
				VolumeCapability: mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER),
			},
			wantCode: codes.InvalidArgument,
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
					FilesystemID: 456,
					Label:        "policy-a",
					Enabled:      true,
					LinodeACL:    testLinodeACL(101, 202, 303),
					SquashPolicy: linodego.NFSSquashPolicyRootSquash,
					Protocols:    []linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4},
				}
				env.client.EXPECT().GetNFSFilesystemAccessPolicy(gomock.Any(), 123, 456).Return(policy, nil)
				env.client.EXPECT().UpdateNFSFilesystemAccessPolicy(gomock.Any(), 123, 456, gomock.Eq(testFilesystemPolicyUpdate("policy-a", true, []int{101, 303}, linodego.NFSSquashPolicyRootSquash))).Return(policy, nil)
				env.client.EXPECT().WaitForNFSFilesystemAccessPolicyStatus(gomock.Any(), 123, 456, linodego.NFSAccessPolicyStatusActive).Return(policy, nil)
			},
		},
		{
			name:    "is idempotent when node is already absent",
			request: &csi.ControllerUnpublishVolumeRequest{VolumeId: testVolumeID, NodeId: "202"},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().GetNFSFilesystemAccessPolicy(gomock.Any(), 123, 456).Return(&linodego.NFSFilesystemAccessPolicy{FilesystemID: 456, Enabled: true, LinodeACL: testLinodeACL(101, 303)}, nil)
			},
		},
		{
			name:    "empty node id is success",
			request: &csi.ControllerUnpublishVolumeRequest{VolumeId: testVolumeID},
		},
		{
			name:    "not found is success",
			request: &csi.ControllerUnpublishVolumeRequest{VolumeId: testVolumeID, NodeId: "202"},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().GetNFSFilesystemAccessPolicy(gomock.Any(), 123, 456).Return(nil, linodeAPIError(http.StatusNotFound))
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
			name:     "empty volume id is invalid argument",
			request:  &csi.ValidateVolumeCapabilitiesRequest{},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "malformed volume id is not found without API call",
			request:  &csi.ValidateVolumeCapabilitiesRequest{VolumeId: "not-a-volume-handle"},
			wantCode: codes.NotFound,
		},
		{
			name: "whitespace volume id is not found without API call",
			request: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId:           " ",
				VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
			},
			wantCode: codes.NotFound,
		},
		{
			name:     "requires capabilities",
			request:  &csi.ValidateVolumeCapabilitiesRequest{VolumeId: testVolumeID},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "returns message for unsupported capability",
			request: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId: testVolumeID,
				VolumeCapabilities: []*csi.VolumeCapability{{
					AccessType: &csi.VolumeCapability_Block{},
					AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
				}},
			},
			assert: func(t *testing.T, response *csi.ValidateVolumeCapabilitiesResponse) {
				t.Helper()
				if response.GetConfirmed() != nil {
					t.Fatalf("expected nil confirmed, got %#v", response.GetConfirmed())
				}
				if response.GetMessage() == "" {
					t.Fatal("expected validation message")
				}
			},
		},
		{
			name: "missing volume is not found",
			request: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId:           testVolumeID,
				VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
			},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().GetNFSFilesystem(gomock.Any(), 123, 456).Return(nil, linodeAPIError(http.StatusNotFound))
			},
			wantCode: codes.NotFound,
		},
		{
			name: "confirms supported mount",
			request: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId:           testVolumeID,
				VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)},
			},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().GetNFSFilesystem(gomock.Any(), 123, 456).Return(&linodego.NFSFilesystem{
					ID:              456,
					SpaceID:         123,
					Region:          "us-east",
					MountTargetFQDN: new("prod-7b.nfs.us-east.linode.com:/pvc-abc-1c8"),
				}, nil)
			},
			assert: func(t *testing.T, response *csi.ValidateVolumeCapabilitiesResponse) {
				t.Helper()
				if response.GetConfirmed() == nil {
					t.Fatal("expected confirmed capabilities")
				}
				if len(response.GetConfirmed().GetVolumeContext()) != 4 {
					t.Fatalf("unexpected volume context %#v", response.GetConfirmed().GetVolumeContext())
				}
				if !reflect.DeepEqual(response.GetConfirmed().GetVolumeCapabilities(), []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)}) {
					t.Fatalf("unexpected confirmed capabilities %#v", response.GetConfirmed().GetVolumeCapabilities())
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
			name:     "requires volume id",
			request:  &csi.ControllerGetVolumeRequest{},
			wantCode: codes.InvalidArgument,
		},
		{
			name:    "returns filesystem metadata",
			request: &csi.ControllerGetVolumeRequest{VolumeId: testVolumeID},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().GetNFSFilesystem(gomock.Any(), 123, 456).Return(&linodego.NFSFilesystem{
					ID:              456,
					SpaceID:         123,
					Region:          "us-east",
					MountTargetFQDN: new("prod-7b.nfs.us-east.linode.com:/pvc-abc-1c8"),
				}, nil)
				env.client.EXPECT().GetNFSFilesystemAccessPolicy(gomock.Any(), 123, 456).Return(&linodego.NFSFilesystemAccessPolicy{
					FilesystemID: 456,
					Enabled:      true,
					LinodeACL:    testLinodeACL(202, 303),
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
				if !reflect.DeepEqual(response.GetStatus().GetPublishedNodeIds(), []string{"202", "303"}) {
					t.Fatalf("unexpected published node ids %#v", response.GetStatus().GetPublishedNodeIds())
				}
			},
		},
		{
			name:    "omits published nodes when policy disabled",
			request: &csi.ControllerGetVolumeRequest{VolumeId: testVolumeID},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().GetNFSFilesystem(gomock.Any(), 123, 456).Return(&linodego.NFSFilesystem{
					ID:              456,
					SpaceID:         123,
					Region:          "us-east",
					MountTargetFQDN: new("prod-7b.nfs.us-east.linode.com:/pvc-abc-1c8"),
				}, nil)
				env.client.EXPECT().GetNFSFilesystemAccessPolicy(gomock.Any(), 123, 456).Return(&linodego.NFSFilesystemAccessPolicy{
					FilesystemID: 456,
					Enabled:      false,
					LinodeACL:    testLinodeACL(202, 303),
				}, nil)
			},
			assert: func(t *testing.T, response *csi.ControllerGetVolumeResponse) {
				t.Helper()
				if response.GetStatus() == nil {
					t.Fatal("expected volume status")
				}
				if len(response.GetStatus().GetPublishedNodeIds()) != 0 {
					t.Fatalf("expected no published node ids, got %#v", response.GetStatus().GetPublishedNodeIds())
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
				{Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{Type: csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME}}},
				{Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{Type: csi.ControllerServiceCapability_RPC_GET_VOLUME}}},
				{Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT}}},
			},
			wantCaps: []*csi.ControllerServiceCapability{
				{Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME}}},
				{Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{Type: csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME}}},
				{Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{Type: csi.ControllerServiceCapability_RPC_GET_VOLUME}}},
				{Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT}}},
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

func TestControllerServerCreateSnapshot(t *testing.T) {
	snapshotListOptions := &linodego.ListOptions{
		PageOptions: &linodego.PageOptions{},
		PageSize:    linodeclient.DefaultListPageSize,
	}
	tests := []struct {
		name         string
		request      *csi.CreateSnapshotRequest
		wantResponse *csi.CreateSnapshotResponse
		setup        func(controllerTestEnv)
		wantErr      error
	}{
		{
			name: "creates a snapshot successfully",
			request: &csi.CreateSnapshotRequest{
				SourceVolumeId: testVolumeID,
				Name:           testVolumeID,
			},
			wantResponse: &csi.CreateSnapshotResponse{
				Snapshot: &csi.Snapshot{
					SizeBytes:      1000,
					SnapshotId:     "123/456/789",
					SourceVolumeId: testVolumeID,
					CreationTime:   timestamppb.New(*timestamp),
					ReadyToUse:     true,
				},
			},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().ListNFSSnapshots(gomock.Any(), 123, 456, gomock.Eq(snapshotListOptions)).Return(nil, nil)
				env.client.EXPECT().CreateNFSSnapshot(gomock.Any(), 123, 456, linodego.NFSSnapshotCreateOptions{Label: testVolumeID}).
					Return(&linodego.NFSSnapshot{
						Status:    linodego.NFSSnapshotStatusCreating,
						Created:   timestamp,
						ID:        789,
						SizeBytes: 1000,
					}, nil)
				env.client.EXPECT().WaitForNFSSnapshotStatus(gomock.Any(), 123, 456, 789, linodego.NFSSnapshotStatusActive).
					Return(&linodego.NFSSnapshot{
						Status:    linodego.NFSSnapshotStatusActive,
						Created:   timestamp,
						ID:        789,
						SizeBytes: 1000,
					}, nil)
			},
		},
		{
			name: "source filesystem not found",
			request: &csi.CreateSnapshotRequest{
				SourceVolumeId: testVolumeID,
				Name:           testVolumeID,
			},
			wantErr: linodeError(linodeAPIError(http.StatusNotFound), "list NFS snapshots"),
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().ListNFSSnapshots(gomock.Any(), 123, 456, gomock.Eq(snapshotListOptions)).
					Return(nil, linodeAPIError(http.StatusNotFound))
			},
		},
		{
			name: "returns an existing same-source snapshot",
			request: &csi.CreateSnapshotRequest{
				SourceVolumeId: testVolumeID,
				Name:           "snapshot-abc",
			},
			wantResponse: &csi.CreateSnapshotResponse{
				Snapshot: &csi.Snapshot{
					SizeBytes:      2000,
					SnapshotId:     "123/456/790",
					SourceVolumeId: testVolumeID,
					CreationTime:   timestamppb.New(*timestamp),
					ReadyToUse:     true,
				},
			},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().ListNFSSnapshots(gomock.Any(), 123, 456, gomock.Eq(snapshotListOptions)).
					Return([]linodego.NFSSnapshot{
						{ID: 789, Label: "another-snapshot", Status: linodego.NFSSnapshotStatusActive, Created: timestamp, SizeBytes: 1000},
						{ID: 790, Label: "snapshot-abc", Status: linodego.NFSSnapshotStatusActive, Created: timestamp, SizeBytes: 2000},
					}, nil)
			},
		},
		{
			name: "reconciles a conflict using the lowercase snapshot label",
			request: &csi.CreateSnapshotRequest{
				SourceVolumeId: testVolumeID,
				Name:           "Sanity-TEST-Snapshot",
			},
			wantResponse: &csi.CreateSnapshotResponse{
				Snapshot: &csi.Snapshot{
					SizeBytes:      2000,
					SnapshotId:     "123/456/790",
					SourceVolumeId: testVolumeID,
					CreationTime:   timestamppb.New(*timestamp),
					ReadyToUse:     true,
				},
			},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().ListNFSSnapshots(gomock.Any(), 123, 456, gomock.Eq(snapshotListOptions)).Return(nil, nil)
				env.client.EXPECT().CreateNFSSnapshot(gomock.Any(), 123, 456, linodego.NFSSnapshotCreateOptions{Label: "sanity-test-snapshot"}).
					Return(nil, linodeAPIError(http.StatusConflict))
				env.client.EXPECT().ListNFSSnapshots(gomock.Any(), 123, 456, gomock.Eq(snapshotListOptions)).
					Return([]linodego.NFSSnapshot{
						{ID: 790, Label: "sanity-test-snapshot", Status: linodego.NFSSnapshotStatusActive, Created: timestamp, SizeBytes: 2000},
					}, nil)
			},
		},
		{
			name: "maps provider conflict to already exists when no matching snapshot exists",
			request: &csi.CreateSnapshotRequest{
				SourceVolumeId: testVolumeID,
				Name:           "snapshot-abc",
			},
			wantErr: status.Error(codes.AlreadyExists, `NFS snapshot "snapshot-abc" already exists with a different source volume`),
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().ListNFSSnapshots(gomock.Any(), 123, 456, gomock.Eq(snapshotListOptions)).Return(nil, nil)
				env.client.EXPECT().CreateNFSSnapshot(gomock.Any(), 123, 456, linodego.NFSSnapshotCreateOptions{Label: "snapshot-abc"}).
					Return(nil, linodeAPIError(http.StatusConflict))
				env.client.EXPECT().ListNFSSnapshots(gomock.Any(), 123, 456, gomock.Eq(snapshotListOptions)).
					Return([]linodego.NFSSnapshot{
						{ID: 790, Label: "another-snapshot", Status: linodego.NFSSnapshotStatusActive, Created: timestamp, SizeBytes: 2000},
					}, nil)
			},
		},
		{
			name: "api error on snapshot creation",
			request: &csi.CreateSnapshotRequest{
				SourceVolumeId: testVolumeID,
				Name:           testVolumeID,
			},
			wantErr: linodeError(&linodego.Error{Code: http.StatusBadGateway}, "create NFS snapshot"),
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().ListNFSSnapshots(gomock.Any(), 123, 456, gomock.Eq(snapshotListOptions)).Return(nil, nil)
				env.client.EXPECT().CreateNFSSnapshot(gomock.Any(), 123, 456, linodego.NFSSnapshotCreateOptions{Label: testVolumeID}).
					Return(nil, &linodego.Error{Code: http.StatusBadGateway})
			},
		},
		{
			name:    "no snapshot name",
			request: &csi.CreateSnapshotRequest{SourceVolumeId: testVolumeID},
			wantErr: errNoSnapshotName,
		},
		{
			name:    "no source volume",
			request: &csi.CreateSnapshotRequest{Name: "snapshot-abc"},
			wantErr: errNoVolumeID,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newControllerTestEnv(t)
			if tt.setup != nil {
				tt.setup(env)
			}

			response, err := env.server.CreateSnapshot(context.Background(), tt.request)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ControllerCreateSnapshot() error = %v, want %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(response, tt.wantResponse) {
				t.Fatalf("ControllerCreateSnapshot() response = %v, want %v", response, tt.wantResponse)
			}
		})
	}
}

func TestControllerServerDeleteSnapshot(t *testing.T) {
	tests := []struct {
		name     string
		request  *csi.DeleteSnapshotRequest
		setup    func(controllerTestEnv)
		wantCode codes.Code
	}{
		{
			name:    "deletes a snapshot successfully",
			request: &csi.DeleteSnapshotRequest{SnapshotId: "123/456/789"},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().DeleteNFSSnapshot(gomock.Any(), 123, 456, 789).Return(nil)
			},
		},
		{
			name:     "no snapshot id",
			request:  &csi.DeleteSnapshotRequest{},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "invalid snapshot id is idempotent",
			request:  &csi.DeleteSnapshotRequest{SnapshotId: "789"},
			wantCode: codes.OK,
		},
		{
			name:    "delete not found is idempotent success",
			request: &csi.DeleteSnapshotRequest{SnapshotId: "123/456/789"},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().DeleteNFSSnapshot(gomock.Any(), 123, 456, 789).Return(linodeAPIError(http.StatusNotFound))
			},
		},
		{
			name:     "delete conflict",
			request:  &csi.DeleteSnapshotRequest{SnapshotId: "123/456/789"},
			wantCode: codes.FailedPrecondition,
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().DeleteNFSSnapshot(gomock.Any(), 123, 456, 789).Return(linodeAPIError(http.StatusConflict))
			},
		},
		{
			name:     "delete unavailable",
			request:  &csi.DeleteSnapshotRequest{SnapshotId: "123/456/789"},
			wantCode: codes.Unavailable,
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().DeleteNFSSnapshot(gomock.Any(), 123, 456, 789).Return(linodeAPIError(http.StatusServiceUnavailable))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newControllerTestEnv(t)
			if tt.setup != nil {
				tt.setup(env)
			}

			response, err := env.server.DeleteSnapshot(context.Background(), tt.request)
			if status.Code(err) != tt.wantCode {
				t.Fatalf("DeleteSnapshot() code = %v, want %v (err = %v)", status.Code(err), tt.wantCode, err)
			}
			if tt.wantCode != codes.OK {
				return
			}
			if !reflect.DeepEqual(response, &csi.DeleteSnapshotResponse{}) {
				t.Fatalf("DeleteSnapshot() response = %#v, want empty response", response)
			}
		})
	}
}

func TestControllerServerListSnapshots(t *testing.T) {
	created := timestamp
	pageOptions := &linodego.ListOptions{
		PageOptions: &linodego.PageOptions{},
		PageSize:    linodeclient.DefaultListPageSize,
	}
	snapshot789 := linodego.NFSSnapshot{
		ID:           789,
		FilesystemID: 456,
		SpaceID:      123,
		Status:       linodego.NFSSnapshotStatusActive,
		Created:      created,
		SizeBytes:    1000,
	}
	snapshot790 := linodego.NFSSnapshot{
		ID:           790,
		FilesystemID: 456,
		SpaceID:      123,
		Status:       linodego.NFSSnapshotStatusCreating,
		Created:      created,
		SizeBytes:    2000,
	}
	tests := []struct {
		name         string
		request      *csi.ListSnapshotsRequest
		setup        func(controllerTestEnv)
		wantCode     codes.Code
		wantResponse *csi.ListSnapshotsResponse
	}{
		{
			name:    "snapshot id hit",
			request: &csi.ListSnapshotsRequest{SnapshotId: "123/456/789"},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().GetNFSSnapshot(gomock.Any(), 123, 456, 789).Return(&snapshot789, nil)
			},
			wantResponse: listSnapshotsResponse(
				csiSnapshotForTest(1000, "123/456/789", testVolumeID, true),
			),
		},
		{
			name:    "snapshot id not found returns empty",
			request: &csi.ListSnapshotsRequest{SnapshotId: "123/456/789"},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().GetNFSSnapshot(gomock.Any(), 123, 456, 789).
					Return(nil, linodeAPIError(http.StatusNotFound))
			},
			wantResponse: emptyListSnapshotsResponse(),
		},
		{
			name:     "invalid snapshot id",
			request:  &csi.ListSnapshotsRequest{SnapshotId: "789"},
			wantCode: codes.InvalidArgument,
		},
		{
			name:    "source volume id listing",
			request: &csi.ListSnapshotsRequest{SourceVolumeId: testVolumeID},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().ListNFSSnapshots(gomock.Any(), 123, 456, gomock.Eq(pageOptions)).
					Return([]linodego.NFSSnapshot{snapshot789, snapshot790}, nil)
			},
			wantResponse: listSnapshotsResponse(
				csiSnapshotForTest(1000, "123/456/789", testVolumeID, true),
				csiSnapshotForTest(2000, "123/456/790", testVolumeID, false),
			),
		},
		{
			name:    "max entries larger than result returns all entries",
			request: &csi.ListSnapshotsRequest{SourceVolumeId: testVolumeID, MaxEntries: 3},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().ListNFSSnapshots(gomock.Any(), 123, 456, gomock.Eq(pageOptions)).
					Return([]linodego.NFSSnapshot{snapshot789, snapshot790}, nil)
			},
			wantResponse: listSnapshotsResponse(
				csiSnapshotForTest(1000, "123/456/789", testVolumeID, true),
				csiSnapshotForTest(2000, "123/456/790", testVolumeID, false),
			),
		},
		{
			name:    "source volume id not found returns empty",
			request: &csi.ListSnapshotsRequest{SourceVolumeId: testVolumeID},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().ListNFSSnapshots(gomock.Any(), 123, 456, gomock.Eq(pageOptions)).
					Return(nil, linodeAPIError(http.StatusNotFound))
			},
			wantResponse: emptyListSnapshotsResponse(),
		},
		{
			name:     "invalid source volume id",
			request:  &csi.ListSnapshotsRequest{SourceVolumeId: "456"},
			wantCode: codes.InvalidArgument,
		},
		{
			name:    "snapshot and source filters match",
			request: &csi.ListSnapshotsRequest{SnapshotId: "123/456/789", SourceVolumeId: testVolumeID},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().GetNFSSnapshot(gomock.Any(), 123, 456, 789).Return(&snapshot789, nil)
			},
			wantResponse: listSnapshotsResponse(
				csiSnapshotForTest(1000, "123/456/789", testVolumeID, true),
			),
		},
		{
			name:         "snapshot and source filters mismatch",
			request:      &csi.ListSnapshotsRequest{SnapshotId: "123/456/789", SourceVolumeId: "321/654"},
			wantResponse: emptyListSnapshotsResponse(),
		},
		{
			name:     "unfiltered listing is rejected",
			request:  &csi.ListSnapshotsRequest{},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "negative max entries is rejected",
			request:  &csi.ListSnapshotsRequest{SourceVolumeId: testVolumeID, MaxEntries: -1},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "malformed starting token is rejected",
			request:  &csi.ListSnapshotsRequest{SourceVolumeId: testVolumeID, StartingToken: "invalid"},
			wantCode: codes.Aborted,
		},
		{
			name:     "negative starting token is rejected",
			request:  &csi.ListSnapshotsRequest{SourceVolumeId: testVolumeID, StartingToken: "-1"},
			wantCode: codes.Aborted,
		},
		{
			name:    "first page returns next token",
			request: &csi.ListSnapshotsRequest{SourceVolumeId: testVolumeID, MaxEntries: 1},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().ListNFSSnapshots(gomock.Any(), 123, 456, gomock.Eq(pageOptions)).
					Return([]linodego.NFSSnapshot{snapshot789, snapshot790}, nil)
			},
			wantResponse: listSnapshotsResponseWithToken(
				"1",
				csiSnapshotForTest(1000, "123/456/789", testVolumeID, true),
			),
		},
		{
			name:    "starting token with zero max returns all remaining entries",
			request: &csi.ListSnapshotsRequest{SourceVolumeId: testVolumeID, StartingToken: "1"},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().ListNFSSnapshots(gomock.Any(), 123, 456, gomock.Eq(pageOptions)).
					Return([]linodego.NFSSnapshot{snapshot789, snapshot790}, nil)
			},
			wantResponse: listSnapshotsResponse(
				csiSnapshotForTest(2000, "123/456/790", testVolumeID, false),
			),
		},
		{
			name:    "final limited page omits next token",
			request: &csi.ListSnapshotsRequest{SourceVolumeId: testVolumeID, StartingToken: "1", MaxEntries: 1},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().ListNFSSnapshots(gomock.Any(), 123, 456, gomock.Eq(pageOptions)).
					Return([]linodego.NFSSnapshot{snapshot789, snapshot790}, nil)
			},
			wantResponse: listSnapshotsResponse(
				csiSnapshotForTest(2000, "123/456/790", testVolumeID, false),
			),
		},
		{
			name:    "out of range starting token is rejected",
			request: &csi.ListSnapshotsRequest{SourceVolumeId: testVolumeID, StartingToken: "2"},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().ListNFSSnapshots(gomock.Any(), 123, 456, gomock.Eq(pageOptions)).
					Return([]linodego.NFSSnapshot{snapshot789, snapshot790}, nil)
			},
			wantCode: codes.Aborted,
		},
		{
			name:    "missing creation timestamp is rejected",
			request: &csi.ListSnapshotsRequest{SourceVolumeId: testVolumeID},
			setup: func(env controllerTestEnv) {
				snapshot := snapshot789
				snapshot.Created = nil
				env.client.EXPECT().ListNFSSnapshots(gomock.Any(), 123, 456, gomock.Eq(pageOptions)).
					Return([]linodego.NFSSnapshot{snapshot}, nil)
			},
			wantCode: codes.Internal,
		},
		{
			name:    "get backend error maps to grpc code",
			request: &csi.ListSnapshotsRequest{SnapshotId: "123/456/789"},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().GetNFSSnapshot(gomock.Any(), 123, 456, 789).
					Return(nil, linodeAPIError(http.StatusServiceUnavailable))
			},
			wantCode: codes.Unavailable,
		},
		{
			name:    "list backend error maps to grpc code",
			request: &csi.ListSnapshotsRequest{SourceVolumeId: testVolumeID},
			setup: func(env controllerTestEnv) {
				env.client.EXPECT().ListNFSSnapshots(gomock.Any(), 123, 456, gomock.Eq(pageOptions)).
					Return(nil, linodeAPIError(http.StatusServiceUnavailable))
			},
			wantCode: codes.Unavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newControllerTestEnv(t)
			if tt.setup != nil {
				tt.setup(env)
			}

			response, err := env.server.ListSnapshots(context.Background(), tt.request)
			if status.Code(err) != tt.wantCode {
				t.Fatalf("ListSnapshots() code = %v, want %v (err = %v)", status.Code(err), tt.wantCode, err)
			}
			if tt.wantCode != codes.OK {
				return
			}
			if !proto.Equal(response, tt.wantResponse) {
				t.Fatalf("ListSnapshots() response = %#v, want %#v", response, tt.wantResponse)
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

func listSnapshotsResponse(snapshots ...*csi.Snapshot) *csi.ListSnapshotsResponse {
	return listSnapshotsResponseWithToken("", snapshots...)
}

func listSnapshotsResponseWithToken(nextToken string, snapshots ...*csi.Snapshot) *csi.ListSnapshotsResponse {
	entries := make([]*csi.ListSnapshotsResponse_Entry, 0, len(snapshots))
	for _, snapshot := range snapshots {
		entries = append(entries, &csi.ListSnapshotsResponse_Entry{Snapshot: snapshot})
	}
	return &csi.ListSnapshotsResponse{Entries: entries, NextToken: nextToken}
}

func emptyListSnapshotsResponse() *csi.ListSnapshotsResponse {
	return &csi.ListSnapshotsResponse{Entries: []*csi.ListSnapshotsResponse_Entry{}}
}

func csiSnapshotForTest(sizeBytes int64, snapshotID, sourceVolumeID string, ready bool) *csi.Snapshot {
	return &csi.Snapshot{
		SizeBytes:      sizeBytes,
		SnapshotId:     snapshotID,
		SourceVolumeId: sourceVolumeID,
		CreationTime:   timestamppb.New(*timestamp),
		ReadyToUse:     ready,
	}
}

func linodeAPIError(code int) error {
	return &linodego.Error{Code: code, Message: http.StatusText(code)}
}
