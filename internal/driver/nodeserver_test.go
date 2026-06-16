package driver

import (
	"context"
	"net/netip"
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	metadataapi "github.com/linode/go-metadata"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/linode/linode-filestorage-csi-driver/mocks"
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
		{name: "NodeUnstageVolume", call: func(server *NodeServer) error {
			_, err := server.NodeUnstageVolume(context.Background(), &csi.NodeUnstageVolumeRequest{})
			return err
		}},
		{name: "NodePublishVolume", call: func(server *NodeServer) error {
			_, err := server.NodePublishVolume(context.Background(), &csi.NodePublishVolumeRequest{})
			return err
		}},
		{name: "NodeUnpublishVolume", call: func(server *NodeServer) error {
			_, err := server.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{})
			return err
		}},
		{name: "NodeGetVolumeStats", call: func(server *NodeServer) error {
			_, err := server.NodeGetVolumeStats(context.Background(), &csi.NodeGetVolumeStatsRequest{})
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
