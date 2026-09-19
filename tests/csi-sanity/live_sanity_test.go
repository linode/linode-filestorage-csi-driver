package csisanity

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	csisanity "github.com/kubernetes-csi/csi-test/v5/pkg/sanity"

	"github.com/linode/linode-filestorage-csi-driver/internal/driver"
)

const nonexistentResourceIDBase = uint64(2_000_000_000)

var _ csisanity.IDGenerator = (*liveIDGenerator)(nil)

type liveIDGenerator struct {
	spaceID    string
	nextVolume atomic.Uint64
	nextNode   atomic.Uint64
}

func (g *liveIDGenerator) GenerateUniqueValidVolumeID() string {
	id := nonexistentResourceIDBase + g.nextVolume.Add(1)
	return g.spaceID + "/" + strconv.FormatUint(id, 10)
}

func (g *liveIDGenerator) GenerateInvalidVolumeID() string {
	return "invalid-volume-id"
}

func (g *liveIDGenerator) GenerateUniqueValidNodeID() string {
	id := nonexistentResourceIDBase + g.nextNode.Add(1)
	return strconv.FormatUint(id, 10)
}

func (g *liveIDGenerator) GenerateInvalidNodeID() string {
	return "invalid-node-id"
}

func TestLiveCSISanity(t *testing.T) {
	if os.Getenv("NFS_CSI_LIVE_SANITY") != "1" {
		t.Skip("set NFS_CSI_LIVE_SANITY=1 to run destructive live CSI sanity")
	}

	spaceID := requiredEnv(t, "NFS_CSI_SANITY_SPACE_ID")
	parsedSpaceID, err := strconv.ParseUint(spaceID, 10, 64)
	if err != nil || parsedSpaceID == 0 {
		t.Fatalf("NFS_CSI_SANITY_SPACE_ID must be a positive integer, got %q", spaceID)
	}

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve live sanity helper directory")
	}
	helperDir := filepath.Dir(filename)

	config := csisanity.NewTestConfig()
	config.Address = requiredEnv(t, "CSI_SANITY_NODE_ENDPOINT")
	config.ControllerAddress = requiredEnv(t, "CSI_SANITY_CONTROLLER_ENDPOINT")
	config.CreateTargetPathCmd = filepath.Join(helperDir, "mkdir_in_pod.sh")
	config.CreateStagingPathCmd = filepath.Join(helperDir, "mkdir_in_pod.sh")
	config.RemoveTargetPathCmd = filepath.Join(helperDir, "rmdir_in_pod.sh")
	config.RemoveStagingPathCmd = filepath.Join(helperDir, "rmdir_in_pod.sh")
	config.CheckPathCmd = filepath.Join(helperDir, "checkpath_in_pod.sh")
	config.CreatePathCmdTimeout = 30 * time.Second
	config.RemovePathCmdTimeout = 30 * time.Second
	config.CheckPathCmdTimeout = 30 * time.Second
	config.TestVolumeAccessType = "mount"
	config.TestVolumeParameters = map[string]string{
		driver.Name + "/space-id": spaceID,
		driver.Name + "/tags":     "csi-sanity",
	}
	config.IDGen = &liveIDGenerator{spaceID: spaceID}

	csisanity.Test(t, config)
}

func requiredEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	return value
}
