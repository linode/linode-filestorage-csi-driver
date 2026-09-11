package driver

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"sync"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"
)

type NonBlockingGRPCServer interface {
	Start(endpoint string, ids csi.IdentityServer, cs csi.ControllerServer, ns csi.NodeServer)
	Wait()
	Stop()
	ForceStop()
}

type nonBlockingGRPCServer struct {
	wg sync.WaitGroup

	serverMu        sync.RWMutex
	server          *grpc.Server
	serverReady     chan struct{}
	serverReadyOnce sync.Once
}

func NewNonBlockingGRPCServer() NonBlockingGRPCServer {
	return &nonBlockingGRPCServer{serverReady: make(chan struct{})}
}

func (s *nonBlockingGRPCServer) Start(endpoint string, ids csi.IdentityServer, cs csi.ControllerServer, ns csi.NodeServer) {
	s.wg.Add(1)
	go s.serve(endpoint, ids, cs, ns)
}

func (s *nonBlockingGRPCServer) Wait() {
	s.wg.Wait()
}

func (s *nonBlockingGRPCServer) Stop() {
	if server := s.serverForStop(); server != nil {
		server.GracefulStop()
	}
}

func (s *nonBlockingGRPCServer) ForceStop() {
	if server := s.serverForStop(); server != nil {
		server.Stop()
	}
}

func (s *nonBlockingGRPCServer) serve(endpoint string, ids csi.IdentityServer, cs csi.ControllerServer, ns csi.NodeServer) {
	defer s.wg.Done()
	defer s.serverReadyOnce.Do(func() { close(s.serverReady) })

	urlObj, err := url.Parse(endpoint)
	if err != nil {
		klog.Fatalf("parse endpoint: %v", err)
	}

	var addr string
	switch scheme := urlObj.Scheme; scheme {
	case "unix":
		addr = urlObj.Path
		if err := os.Remove(addr); err != nil && !os.IsNotExist(err) {
			klog.Fatalf("remove socket %s: %v", addr, err)
		}
	case "tcp":
		addr = urlObj.Host
	default:
		klog.Fatalf("endpoint scheme not supported: %s", urlObj.Scheme)
	}

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), urlObj.Scheme, addr)
	if err != nil {
		klog.Fatalf("listen: %v", err)
	}

	server := grpc.NewServer(grpc.ChainUnaryInterceptor(logGRPC))
	if ids != nil {
		csi.RegisterIdentityServer(server, ids)
	}
	if cs != nil {
		csi.RegisterControllerServer(server, cs)
	}
	if ns != nil {
		csi.RegisterNodeServer(server, ns)
	}
	s.serverMu.Lock()
	s.server = server
	s.serverMu.Unlock()
	s.serverReadyOnce.Do(func() { close(s.serverReady) })

	if err := server.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		klog.Fatalf("serve: %v", err)
	}

	if urlObj.Scheme == "unix" {
		if err := listener.Close(); err != nil {
			klog.ErrorS(err, "close unix listener")
		}
	}
}

func (s *nonBlockingGRPCServer) serverForStop() *grpc.Server {
	<-s.serverReady
	s.serverMu.RLock()
	defer s.serverMu.RUnlock()
	return s.server
}

func logGRPC(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	klog.V(3).InfoS("grpc call", "method", info.FullMethod)
	klog.V(5).InfoS("grpc request", "method", info.FullMethod, "request", req)
	resp, err := handler(ctx, req)
	if err != nil {
		klog.ErrorS(err, "grpc error", "method", info.FullMethod)
		return resp, err
	}
	klog.V(5).InfoS("grpc response", "method", info.FullMethod, "response", resp)
	return resp, nil
}
