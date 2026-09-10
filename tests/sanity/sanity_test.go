package sanity

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	csisanity "github.com/kubernetes-csi/csi-test/v5/pkg/sanity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/linode/linode-filestorage-csi-driver/internal/driver"
)

var _ csisanity.IDGenerator = (*numericIDGenerator)(nil)

type numericIDGenerator struct {
	nextVolumeID atomic.Uint64
	nextNodeID   atomic.Uint64
}

func (g *numericIDGenerator) GenerateUniqueValidVolumeID() string {
	id := g.nextVolumeID.Add(1) + 900000
	return fmt.Sprintf("1/%d", id)
}

func (g *numericIDGenerator) GenerateInvalidVolumeID() string {
	return "invalid-volume-id"
}

func (g *numericIDGenerator) GenerateUniqueValidNodeID() string {
	if g.nextNodeID.Add(1) <= 2 {
		return "123"
	}
	return "999999"
}

func (g *numericIDGenerator) GenerateInvalidNodeID() string {
	return "invalid-node-id"
}

func TestCSISanity(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Errorf("CSI sanity test panicked: %v", recovered)
		}
	}()

	root := t.TempDir()
	endpoint := "unix://" + filepath.Join(root, "csi.sock")

	fakeClient := newFakeSanityLinodeClient()
	fakeInstanceMetadata := newFakeSanityInstanceMetadataClient()
	fakeKubeNodes := newFakeSanityKubeNodeClient()
	fakeMetadata := driver.NewTestMetadataProvider(
		"sanity-node",
		fakeInstanceMetadata,
		fakeKubeNodes,
		fakeClient,
	)
	fakeMounter := newFakeSanitySafeMounter(newFakeSanityMounter())
	identity, controller, node, err := driver.NewTestServices(
		context.Background(),
		fakeClient,
		fakeMetadata,
		fakeMounter,
	)
	if err != nil {
		t.Fatalf("construct test CSI services: %v", err)
	}

	server := driver.NewNonBlockingGRPCServer()
	server.Start(endpoint, identity, controller, node)
	t.Cleanup(func() {
		server.Stop()
		server.Wait()
	})

	readyContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := waitForEndpoint(readyContext, endpoint); err != nil {
		t.Fatalf("wait for CSI endpoint: %v", err)
	}

	config := csisanity.NewTestConfig()
	config.Address = endpoint
	config.ControllerAddress = ""
	config.TargetPath = filepath.Join(root, "target")
	config.StagingPath = filepath.Join(root, "staging")
	config.TestVolumeSize = fakeSanityFilesystemCapacity
	config.TestVolumeParameters = map[string]string{"space-id": "1"}
	config.TestVolumeAccessType = "mount"
	config.IDGen = &numericIDGenerator{}

	csisanity.Test(t, config)
}

func waitForEndpoint(ctx context.Context, endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse endpoint: %w", err)
	}
	if parsed.Scheme != "unix" {
		return fmt.Errorf("unsupported readiness endpoint scheme %q", parsed.Scheme)
	}

	conn, err := grpc.NewClient(
		endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", parsed.Path)
		}),
	)
	if err != nil {
		return err
	}
	defer conn.Close()

	conn.Connect()
	for {
		state := conn.GetState()
		if state == connectivity.Ready {
			return nil
		}
		if !conn.WaitForStateChange(ctx, state) {
			if err := ctx.Err(); err != nil {
				return err
			}
			return fmt.Errorf("CSI endpoint did not become ready")
		}
	}
}
