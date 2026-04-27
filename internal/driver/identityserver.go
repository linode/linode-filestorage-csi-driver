package driver

import (
	"context"
	"fmt"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"k8s.io/klog/v2"
)

type IdentityServer struct {
	driver *LinodeDriver
	csi.UnimplementedIdentityServer
}

func NewIdentityServer(ctx context.Context, driver *LinodeDriver) (*IdentityServer, error) {
	_ = ctx
	klog.V(4).InfoS("creating identity server")
	if driver == nil {
		return nil, fmt.Errorf("linode driver cannot be nil")
	}
	return &IdentityServer{driver: driver}, nil
}

func (s *IdentityServer) GetPluginInfo(ctx context.Context, _ *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	_ = ctx
	klog.V(4).InfoS("handling identity rpc", "method", "GetPluginInfo")

	if s.driver.name == "" {
		return nil, status.Error(codes.Unavailable, "driver name not configured")
	}

	return &csi.GetPluginInfoResponse{
		Name:          s.driver.name,
		VendorVersion: s.driver.vendorVersion,
	}, nil
}

func (s *IdentityServer) GetPluginCapabilities(ctx context.Context, _ *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	_ = ctx
	klog.V(4).InfoS("handling identity rpc", "method", "GetPluginCapabilities")

	return &csi.GetPluginCapabilitiesResponse{Capabilities: s.driver.pluginCaps}, nil
}

func (s *IdentityServer) Probe(ctx context.Context, _ *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	_ = ctx
	klog.V(4).InfoS("handling identity rpc", "method", "Probe")

	s.driver.readyMu.Lock()
	defer s.driver.readyMu.Unlock()

	return &csi.ProbeResponse{
		Ready: &wrapperspb.BoolValue{Value: s.driver.ready},
	}, nil
}
