package cache

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/linode/linodego/v2"
	"k8s.io/klog/v2"

	linodeclient "github.com/linode/linode-filestorage-csi-driver/pkg/linode-client"
)

const defaultFilesystemTTLSeconds = 15

type filesystemCache struct {
	sync.Mutex
	filesystemsForSpace map[int][]linodego.NFSFilesystem
	lastUpdate          time.Time
	ttl                 time.Duration

	spaces *Spaces
}

func (fsc *filesystemCache) refreshFilesystems(ctx context.Context, client linodeclient.LinodeClient, opts *linodego.ListOptions) error {
	if time.Since(fsc.lastUpdate) < fsc.ttl {
		return nil
	}

	// refresh spaces if needed

	spaces, err := fsc.spaces.ListAllSpaces(ctx, opts)
	if err != nil {
		return err
	}

	fsc.Lock()
	defer fsc.Unlock()

	fses := map[int][]linodego.NFSFilesystem{}
	for idx := range spaces {
		spaceID := spaces[idx].ID
		fsList, err := client.ListNFSFilesystems(ctx, spaceID, &linodego.ListOptions{PageSize: linodeclient.MaxPageSize})
		if err != nil {
			return err
		}
		fses[spaceID] = fsList
	}

	fsc.filesystemsForSpace = fses
	fsc.lastUpdate = time.Now()

	return nil
}

type Filesystems struct {
	linodeClient    linodeclient.LinodeClient
	filesystemCache *filesystemCache
}

func NewFilesystems(linodeClient linodeclient.LinodeClient, spaces *Spaces) *Filesystems {
	ttl := defaultFilesystemTTLSeconds
	if raw, ok := os.LookupEnv("LINODE_NFS_FILESYSTEM_CACHE_TTL_SECONDS"); ok {
		if t, err := strconv.Atoi(raw); t > 0 && err == nil {
			ttl = t
		}
	}
	klog.V(3).Infof("TTL for NFS Filesystem Cache set to %d", ttl)

	return &Filesystems{
		linodeClient: linodeClient,
		filesystemCache: &filesystemCache{
			filesystemsForSpace: make(map[int][]linodego.NFSFilesystem, 0),
			ttl:                 time.Duration(ttl) * time.Second,

			spaces: spaces,
		},
	}
}

// ListSpaceFilesystems returns filesystems for given space id
// It refreshes filesystemsCache if it has expired
func (f *Filesystems) ListSpaceFilesystems(ctx context.Context, id int, opts *linodego.ListOptions) ([]linodego.NFSFilesystem, error) {
	err := f.filesystemCache.refreshFilesystems(ctx, f.linodeClient, opts)
	if err != nil {
		return nil, err
	}

	return f.filesystemCache.filesystemsForSpace[id], nil
}

func (f *Filesystems) GetNFSFilesystem(ctx context.Context, spaceID, filesystemID int) (*linodego.NFSFilesystem, error) {
	fsesForSpace, err := f.ListSpaceFilesystems(ctx, spaceID, nil)
	if err != nil {
		return nil, err
	}
	for idx := range fsesForSpace {
		if fsesForSpace[idx].ID == filesystemID {
			return &fsesForSpace[idx], nil
		}
	}
	return nil, fmt.Errorf("no filesystems found for space ID %d", spaceID)
}

func (f *Filesystems) CreateNFSFilesystem(ctx context.Context, spaceID int, opts linodego.NFSFilesystemCreateOptions) (*linodego.NFSFilesystem, error) {
	filesystem, err := f.linodeClient.CreateNFSFilesystem(ctx, spaceID, opts)
	if err != nil {
		return nil, err
	}
	f.filesystemCache.Lock()
	defer f.filesystemCache.Unlock()

	f.filesystemCache.filesystemsForSpace[spaceID] = append(f.filesystemCache.filesystemsForSpace[spaceID], *filesystem)

	return filesystem, nil
}

func (f *Filesystems) DeleteNFSFilesystem(ctx context.Context, spaceID, filesystemID int) error {
	if err := f.linodeClient.DeleteNFSFilesystem(ctx, spaceID, filesystemID); err != nil {
		return err
	}
	f.filesystemCache.Lock()
	defer f.filesystemCache.Unlock()

	fses := f.filesystemCache.filesystemsForSpace[spaceID]
	newFSes := []linodego.NFSFilesystem{}
	for idx := range fses {
		if fses[idx].ID != filesystemID {
			newFSes = append(newFSes, fses[idx])
		}
	}
	f.filesystemCache.filesystemsForSpace[spaceID] = newFSes

	return nil
}
