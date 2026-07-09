package cache

import (
	"context"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/linode/linodego/v2"
	"k8s.io/klog/v2"

	linodeclient "github.com/linode/linode-filestorage-csi-driver/pkg/linode-client"
)

const defaultSpaceTTLSeconds = 15

type spaceCache struct {
	sync.Mutex
	spaces     map[int]*linodego.NFSSpace
	lastUpdate time.Time
	ttl        time.Duration
}

func (sc *spaceCache) refreshSpaces(ctx context.Context, client linodeclient.LinodeClient, opts *linodego.ListOptions) error {
	if time.Since(sc.lastUpdate) < sc.ttl {
		return nil
	}

	sc.Lock()
	defer sc.Unlock()

	if opts == nil {
		opts = &linodego.ListOptions{PageSize: linodeclient.MaxPageSize}
	}
	spaceList, err := client.ListNFSSpaces(ctx, opts)
	if err != nil {
		return err
	}

	spaces := make(map[int]*linodego.NFSSpace, len(spaceList))
	for idx := range spaceList {
		space := spaceList[idx]
		spaces[space.ID] = &space
	}
	sc.spaces = spaces
	sc.lastUpdate = time.Now()

	return nil
}

type Spaces struct {
	linodeClient linodeclient.LinodeClient
	spaceCache   *spaceCache
}

func NewSpaces(linodeClient linodeclient.LinodeClient) *Spaces {
	ttl := defaultSpaceTTLSeconds
	if raw, ok := os.LookupEnv("LINODE_NFS_SPACE_CACHE_TTL_SECONDS"); ok {
		if t, err := strconv.Atoi(raw); t > 0 && err == nil {
			ttl = t
		}
	}
	klog.V(3).Infof("TTL for NFS Space Cache set to %d", ttl)

	return &Spaces{
		linodeClient: linodeClient,
		spaceCache: &spaceCache{
			spaces: make(map[int]*linodego.NFSSpace),
			ttl:    time.Duration(ttl) * time.Second,
		},
	}
}

// ListAllSpaces returns all spaces in spaceCache, refreshing if needed
func (s *Spaces) ListAllSpaces(ctx context.Context, opts *linodego.ListOptions) ([]linodego.NFSSpace, error) {
	if err := s.spaceCache.refreshSpaces(ctx, s.linodeClient, opts); err != nil {
		return nil, err
	}

	spaces := []linodego.NFSSpace{}
	for _, space := range s.spaceCache.spaces {
		spaces = append(spaces, *space)
	}
	return spaces, nil
}

// GetNFSSpace looks up an NFS Space by its ID
func (s *Spaces) GetNFSSpace(ctx context.Context, spaceID int) (*linodego.NFSSpace, error) {
	if err := s.spaceCache.refreshSpaces(ctx, s.linodeClient, nil); err != nil {
		return nil, err
	}
	return s.spaceCache.spaces[spaceID], nil
}
