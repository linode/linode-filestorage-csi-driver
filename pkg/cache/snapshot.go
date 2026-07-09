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

const defaultSnapshotTTLSeconds = 15

type snapshotCache struct {
	sync.Mutex
	snapshotsForFilesystem map[int][]linodego.NFSSnapshot
	lastUpdate             time.Time
	ttl                    time.Duration

	filesystems *Filesystems
	spaces      *Spaces
}

func (ssc *snapshotCache) refreshSnapshots(ctx context.Context, client linodeclient.LinodeClient, opts *linodego.ListOptions) error {
	ssc.Lock()
	defer ssc.Unlock()

	if time.Since(ssc.lastUpdate) < ssc.ttl {
		return nil
	}

	if opts == nil {
		opts = &linodego.ListOptions{PageSize: linodeclient.MaxPageSize}
	}
	snapshots := map[int][]linodego.NFSSnapshot{}
	// refresh the spaces if expired
	if err := ssc.spaces.spaceCache.refreshSpaces(ctx, client, opts); err != nil {
		return err
	}
	// get all the cached spaces
	spaces, err := ssc.spaces.ListAllSpaces(ctx, opts)
	if err != nil {
		return err
	}

	for idx := range spaces {
		// refresh the filesystems if expired
		if err := ssc.filesystems.filesystemCache.refreshFilesystems(ctx, client, opts); err != nil {
			return err
		}
		// get all the filesystems for the space
		filesystems, err := ssc.filesystems.ListSpaceFilesystems(ctx, spaces[idx].ID, opts)
		if err != nil {
			return err
		}

		for idy := range filesystems {
			// get all the snapshots for the filesystem
			snapList, err := client.ListNFSSnapshots(ctx, spaces[idx].ID, filesystems[idy].ID, opts)
			if err != nil {
				return err
			}
			snapshots[filesystems[idy].ID] = snapList
		}
	}

	ssc.snapshotsForFilesystem = snapshots
	ssc.lastUpdate = time.Now()

	return nil
}

type Snapshots struct {
	linodeClient  linodeclient.LinodeClient
	snapshotCache *snapshotCache
}

func NewSnapshots(linodeClient linodeclient.LinodeClient, spacesCache *Spaces, filesystemCache *Filesystems) *Snapshots {
	ttl := defaultSnapshotTTLSeconds
	if raw, ok := os.LookupEnv("LINODE_NFS_SNAPSHOT_CACHE_TTL_SECONDS"); ok {
		if t, err := strconv.Atoi(raw); t > 0 && err == nil {
			ttl = t
		}
	}
	klog.V(3).Infof("TTL for NFS Snapshot Cache set to %d", ttl)

	return &Snapshots{
		linodeClient: linodeClient,
		snapshotCache: &snapshotCache{
			snapshotsForFilesystem: make(map[int][]linodego.NFSSnapshot),
			ttl:                    time.Duration(ttl) * time.Second,

			filesystems: filesystemCache,
			spaces:      spacesCache,
		},
	}
}

// ListFilesystemSnapshots returns snapshots for given filesystem ID, refreshing snapshotsCache if expired
func (s *Snapshots) ListFilesystemSnapshots(ctx context.Context, id int) ([]linodego.NFSSnapshot, error) {
	if err := s.snapshotCache.refreshSnapshots(ctx, s.linodeClient, nil); err != nil {
		return nil, err
	}
	filesystemSnapshots, ok := s.snapshotCache.snapshotsForFilesystem[id]
	if !ok {
		return nil, fmt.Errorf("no filesystems found for space ID %d", id)
	}

	return filesystemSnapshots, nil
}

func (s *Snapshots) CreateNFSSnapshot(ctx context.Context, spaceID, filesystemID int, options linodego.NFSSnapshotCreateOptions) (*linodego.NFSSnapshot, error) {
	snapshot, err := s.linodeClient.CreateNFSSnapshot(ctx, spaceID, filesystemID, options)
	if err != nil {
		return nil, err
	}
	s.snapshotCache.Lock()
	defer s.snapshotCache.Unlock()

	s.snapshotCache.snapshotsForFilesystem[filesystemID] = append(s.snapshotCache.snapshotsForFilesystem[filesystemID], *snapshot)

	return snapshot, nil
}

func (s *Snapshots) DeleteNFSSnapshot(ctx context.Context, spaceID, filesystemID, snapshotID int) error {
	if err := s.linodeClient.DeleteNFSSnapshot(ctx, spaceID, filesystemID, snapshotID); err != nil {
		return err
	}
	s.snapshotCache.Lock()
	defer s.snapshotCache.Unlock()

	snaps := s.snapshotCache.snapshotsForFilesystem[filesystemID]
	newSnaps := []linodego.NFSSnapshot{}
	for idx := range snaps {
		if snaps[idx].ID != snapshotID {
			newSnaps = append(newSnaps, snaps[idx])
		}
	}
	s.snapshotCache.snapshotsForFilesystem[filesystemID] = newSnaps

	return nil
}
