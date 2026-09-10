package sanity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/linode/linodego/v2"

	linodeclient "github.com/linode/linode-filestorage-csi-driver/pkg/linode-client"
)

const (
	fakeSanitySpaceID                  = 1
	fakeSanityFilesystemStartID        = 10000
	fakeSanitySnapshotStartID          = 20000
	fakeSanityFilesystemCapacity int64 = 1 << 30
)

var _ linodeclient.LinodeClient = (*fakeSanityLinodeClient)(nil)

type fakeSanityLinodeClient struct {
	mu sync.Mutex

	space       linodego.NFSSpace
	spacePolicy linodego.NFSSpaceAccessPolicy

	filesystems      map[int]linodego.NFSFilesystem
	filesystemByName map[string]int
	filesystemPolicy map[int]linodego.NFSFilesystemAccessPolicy
	snapshots        map[int]linodego.NFSSnapshot
	// CSI sanity expects same-name snapshots from different source volumes to
	// return AlreadyExists; the current NFS OpenAPI documents per-filesystem
	// uniqueness. Keep this index global until that contract is resolved.
	snapshotByName   map[string]int
	nextFilesystemID int
	nextSnapshotID   int
}

func newFakeSanityLinodeClient() *fakeSanityLinodeClient {
	now := time.Now().UTC()
	return &fakeSanityLinodeClient{
		space: linodego.NFSSpace{
			ID:      fakeSanitySpaceID,
			Label:   "sanity-space",
			Status:  linodego.NFSSpaceStatusActive,
			Created: &now,
			Updated: &now,
		},
		spacePolicy: linodego.NFSSpaceAccessPolicy{
			SpaceID:  fakeSanitySpaceID,
			Label:    "sanity-space-policy",
			Enabled:  false,
			VPCACL:   []linodego.NFSSpaceAccessPolicyVPC{{ID: sanityVPCID}},
			MTLSMode: linodego.NFSMTLSModeDisabled,
			Status:   linodego.NFSAccessPolicyStatusActive,
			Created:  &now,
			Updated:  &now,
		},
		filesystems:      make(map[int]linodego.NFSFilesystem),
		filesystemByName: make(map[string]int),
		filesystemPolicy: make(map[int]linodego.NFSFilesystemAccessPolicy),
		snapshots:        make(map[int]linodego.NFSSnapshot),
		snapshotByName:   make(map[string]int),
		nextFilesystemID: fakeSanityFilesystemStartID,
		nextSnapshotID:   fakeSanitySnapshotStartID,
	}
}

func (c *fakeSanityLinodeClient) GetInstance(_ context.Context, linodeID int) (*linodego.Instance, error) {
	if linodeID != sanityNodeID {
		return nil, fakeSanityNotFound("Linode instance", linodeID)
	}
	return &linodego.Instance{
		ID:                  sanityNodeID,
		Region:              sanityRegion,
		Status:              linodego.InstanceRunning,
		InterfaceGeneration: linodego.GenerationLinode,
	}, nil
}

func (c *fakeSanityLinodeClient) ListInterfaces(_ context.Context, linodeID int, _ *linodego.ListOptions) ([]linodego.LinodeInterface, error) {
	if linodeID != sanityNodeID {
		return nil, fakeSanityNotFound("Linode instance", linodeID)
	}
	return []linodego.LinodeInterface{{ID: 1, VPC: &linodego.VPCInterface{VPCID: sanityVPCID, SubnetID: 1}}}, nil
}

func (c *fakeSanityLinodeClient) ListInstanceConfigs(_ context.Context, linodeID int, _ *linodego.ListOptions) ([]linodego.InstanceConfig, error) {
	if linodeID != sanityNodeID {
		return nil, fakeSanityNotFound("Linode instance", linodeID)
	}
	vpcID := sanityVPCID
	return []linodego.InstanceConfig{{
		ID: 1,
		Interfaces: []linodego.InstanceConfigInterface{{
			ID: 1, Purpose: linodego.InterfacePurposeVPC, Active: true, VPCID: &vpcID,
		}},
	}}, nil
}

func (c *fakeSanityLinodeClient) ListNFSSpaces(_ context.Context, opts *linodego.ListOptions) ([]linodego.NFSSpace, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	filter, err := sanityListFilter(opts)
	if err != nil {
		return nil, err
	}
	if filter.label != "" && filter.label != c.space.Label {
		return []linodego.NFSSpace{}, nil
	}
	return []linodego.NFSSpace{cloneSanitySpace(&c.space)}, nil
}

func (c *fakeSanityLinodeClient) GetNFSSpace(_ context.Context, spaceID int) (*linodego.NFSSpace, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.requireSpaceLocked(spaceID); err != nil {
		return nil, err
	}
	space := cloneSanitySpace(&c.space)
	return &space, nil
}

func (c *fakeSanityLinodeClient) ListNFSFilesystems(_ context.Context, spaceID int, opts *linodego.ListOptions) ([]linodego.NFSFilesystem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.requireSpaceLocked(spaceID); err != nil {
		return nil, err
	}
	filter, err := sanityListFilter(opts)
	if err != nil {
		return nil, err
	}
	result := make([]linodego.NFSFilesystem, 0, len(c.filesystems))
	for filesystemID := range c.filesystems {
		filesystem := c.filesystems[filesystemID]
		if filter.label != "" && filesystem.Label != filter.label {
			continue
		}
		if filter.region != "" && filesystem.Region != filter.region {
			continue
		}
		result = append(result, cloneSanityFilesystem(&filesystem))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (c *fakeSanityLinodeClient) GetNFSFilesystem(_ context.Context, spaceID, filesystemID int) (*linodego.NFSFilesystem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	filesystem, err := c.filesystemLocked(spaceID, filesystemID)
	if err != nil {
		return nil, err
	}
	result := cloneSanityFilesystem(&filesystem)
	return &result, nil
}

func (c *fakeSanityLinodeClient) GetNFSFilesystemByID(_ context.Context, filesystemID int) (*linodego.NFSFilesystem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	filesystem, ok := c.filesystems[filesystemID]
	if !ok {
		return nil, fakeSanityNotFound("NFS filesystem", filesystemID)
	}
	result := cloneSanityFilesystem(&filesystem)
	return &result, nil
}

func (c *fakeSanityLinodeClient) CreateNFSFilesystem(_ context.Context, spaceID int, opts linodego.NFSFilesystemCreateOptions) (*linodego.NFSFilesystem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.requireSpaceLocked(spaceID); err != nil {
		return nil, err
	}
	if err := validateSanityFilesystemCreate(opts); err != nil {
		return nil, err
	}
	name := fakeSanityFilesystemName(spaceID, opts.Label)
	if filesystemID, ok := c.filesystemByName[name]; ok {
		filesystem := c.filesystems[filesystemID]
		if !sanityFilesystemCreateCompatible(&filesystem, opts) {
			return nil, fakeSanityConflict("NFS filesystem %q already exists with incompatible parameters", opts.Label)
		}
		result := cloneSanityFilesystem(&filesystem)
		return &result, nil
	}
	filesystem := newSanityFilesystem(c.nextFilesystemID, spaceID, opts, nil)
	deactivateSanityFilesystem(&filesystem)
	c.nextFilesystemID++
	c.filesystems[filesystem.ID] = filesystem
	c.filesystemByName[name] = filesystem.ID
	c.filesystemPolicy[filesystem.ID] = newSanityFilesystemPolicy(&filesystem)
	result := cloneSanityFilesystem(&filesystem)
	return &result, nil
}

func (c *fakeSanityLinodeClient) WaitForNFSFilesystemStatus(_ context.Context, spaceID, filesystemID int, status linodego.NFSFilesystemStatus) (*linodego.NFSFilesystem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	filesystem, err := c.filesystemLocked(spaceID, filesystemID)
	if err != nil {
		return nil, err
	}
	if status != linodego.NFSFilesystemStatusActive {
		return nil, fakeSanityInvalid("NFS filesystem %d is only available in active status", filesystemID)
	}
	activateSanityFilesystem(&filesystem)
	c.filesystems[filesystemID] = filesystem
	result := cloneSanityFilesystem(&filesystem)
	return &result, nil
}

func (c *fakeSanityLinodeClient) DeleteNFSFilesystem(_ context.Context, spaceID, filesystemID int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	filesystem, err := c.filesystemLocked(spaceID, filesystemID)
	if err != nil {
		return err
	}
	delete(c.filesystems, filesystemID)
	delete(c.filesystemByName, fakeSanityFilesystemName(spaceID, filesystem.Label))
	delete(c.filesystemPolicy, filesystemID)
	for snapshotID := range c.snapshots {
		snapshot := c.snapshots[snapshotID]
		if snapshot.SpaceID == spaceID && snapshot.FilesystemID == filesystemID {
			delete(c.snapshots, snapshotID)
			delete(c.snapshotByName, snapshot.Label)
		}
	}
	return nil
}

func (c *fakeSanityLinodeClient) GetNFSSpaceAccessPolicy(_ context.Context, spaceID int) (*linodego.NFSSpaceAccessPolicy, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.requireSpaceLocked(spaceID); err != nil {
		return nil, err
	}
	policy := cloneSanitySpacePolicy(&c.spacePolicy)
	return &policy, nil
}

func (c *fakeSanityLinodeClient) UpdateNFSSpaceAccessPolicy(_ context.Context, spaceID int, opts linodego.NFSSpaceAccessPolicyUpdateOptions) (*linodego.NFSSpaceAccessPolicy, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.requireSpaceLocked(spaceID); err != nil {
		return nil, err
	}
	if err := validateSanitySpacePolicy(opts); err != nil {
		return nil, err
	}
	policy := c.spacePolicy
	if opts.Label != nil {
		policy.Label = *opts.Label
	}
	if opts.Enabled != nil {
		policy.Enabled = *opts.Enabled
	}
	if opts.VPCs != nil {
		policy.VPCACL = make([]linodego.NFSSpaceAccessPolicyVPC, 0, len(*opts.VPCs))
		for i := range *opts.VPCs {
			vpc := &(*opts.VPCs)[i]
			subnets := make([]linodego.NFSSpaceAccessPolicyVPCSubnet, 0, len(vpc.Subnets))
			for _, subnetID := range vpc.Subnets {
				subnets = append(subnets, linodego.NFSSpaceAccessPolicyVPCSubnet{ID: subnetID})
			}
			policy.VPCACL = append(policy.VPCACL, linodego.NFSSpaceAccessPolicyVPC{ID: vpc.ID, Subnets: subnets})
		}
	}
	if opts.MTLSCACert != nil {
		policy.MTLSCACert = cloneSanityString(opts.MTLSCACert)
	}
	if opts.MTLSMode != nil {
		policy.MTLSMode = *opts.MTLSMode
	}
	policy.Status = linodego.NFSAccessPolicyStatusUpdating
	policy.Updated = sanityNow()
	c.spacePolicy = policy
	result := cloneSanitySpacePolicy(&policy)
	return &result, nil
}

func (c *fakeSanityLinodeClient) WaitForNFSSpaceAccessPolicyStatus(_ context.Context, spaceID int, status linodego.NFSAccessPolicyStatus) (*linodego.NFSSpaceAccessPolicy, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.requireSpaceLocked(spaceID); err != nil {
		return nil, err
	}
	if status != linodego.NFSAccessPolicyStatusActive {
		return nil, fakeSanityInvalid("NFS space access policy is only available in active status")
	}
	policy := c.spacePolicy
	policy.Status = linodego.NFSAccessPolicyStatusActive
	c.spacePolicy = policy
	result := cloneSanitySpacePolicy(&policy)
	return &result, nil
}

func (c *fakeSanityLinodeClient) GetNFSFilesystemAccessPolicy(_ context.Context, spaceID, filesystemID int) (*linodego.NFSFilesystemAccessPolicy, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.filesystemLocked(spaceID, filesystemID); err != nil {
		return nil, err
	}
	policy := c.filesystemPolicy[filesystemID]
	result := cloneSanityFilesystemPolicy(&policy)
	return &result, nil
}

func (c *fakeSanityLinodeClient) UpdateNFSFilesystemAccessPolicy(_ context.Context, spaceID, filesystemID int, opts linodego.NFSFilesystemAccessPolicyUpdateOptions) (*linodego.NFSFilesystemAccessPolicy, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.filesystemLocked(spaceID, filesystemID); err != nil {
		return nil, err
	}
	if err := validateSanityFilesystemPolicy(opts); err != nil {
		return nil, err
	}
	policy := c.filesystemPolicy[filesystemID]
	if opts.Label != nil {
		policy.Label = *opts.Label
	}
	if opts.Enabled != nil {
		policy.Enabled = *opts.Enabled
	}
	if opts.LinodeIDs != nil {
		policy.LinodeACL = make([]linodego.NFSFilesystemAccessPolicyLinode, 0, len(*opts.LinodeIDs))
		for _, linodeID := range *opts.LinodeIDs {
			if linodeID != sanityNodeID {
				return nil, fakeSanityNotFound("Linode instance", linodeID)
			}
			policy.LinodeACL = append(policy.LinodeACL, linodego.NFSFilesystemAccessPolicyLinode{ID: linodeID})
		}
	}
	if opts.SquashPolicy != nil {
		policy.SquashPolicy = *opts.SquashPolicy
	}
	if opts.Protocols != nil {
		policy.Protocols = append([]linodego.NFSProtocolVersion(nil), (*opts.Protocols)...)
	}
	policy.Status = linodego.NFSAccessPolicyStatusUpdating
	policy.Updated = sanityNow()
	c.filesystemPolicy[filesystemID] = policy
	result := cloneSanityFilesystemPolicy(&policy)
	return &result, nil
}

func (c *fakeSanityLinodeClient) WaitForNFSFilesystemAccessPolicyStatus(_ context.Context, spaceID, filesystemID int, status linodego.NFSAccessPolicyStatus) (*linodego.NFSFilesystemAccessPolicy, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.filesystemLocked(spaceID, filesystemID); err != nil {
		return nil, err
	}
	if status != linodego.NFSAccessPolicyStatusActive {
		return nil, fakeSanityInvalid("NFS filesystem access policy is only available in active status")
	}
	policy := c.filesystemPolicy[filesystemID]
	policy.Status = linodego.NFSAccessPolicyStatusActive
	c.filesystemPolicy[filesystemID] = policy
	result := cloneSanityFilesystemPolicy(&policy)
	return &result, nil
}

func (c *fakeSanityLinodeClient) ListNFSSnapshots(_ context.Context, spaceID, filesystemID int, _ *linodego.ListOptions) ([]linodego.NFSSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.filesystemLocked(spaceID, filesystemID); err != nil {
		return nil, err
	}
	result := make([]linodego.NFSSnapshot, 0)
	for snapshotID := range c.snapshots {
		snapshot := c.snapshots[snapshotID]
		if snapshot.SpaceID == spaceID && snapshot.FilesystemID == filesystemID {
			result = append(result, cloneSanitySnapshot(&snapshot))
		}
	}
	sort.Slice(result, func(left, right int) bool {
		switch {
		case result[left].Created == nil && result[right].Created == nil:
			return result[left].ID > result[right].ID
		case result[left].Created == nil:
			return false
		case result[right].Created == nil:
			return true
		default:
			return result[left].Created.After(*result[right].Created)
		}
	})
	return result, nil
}

func (c *fakeSanityLinodeClient) GetNFSSnapshot(_ context.Context, spaceID, filesystemID, snapshotID int) (*linodego.NFSSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot, err := c.snapshotLocked(spaceID, filesystemID, snapshotID)
	if err != nil {
		return nil, err
	}
	result := cloneSanitySnapshot(&snapshot)
	return &result, nil
}

func (c *fakeSanityLinodeClient) CreateNFSSnapshot(_ context.Context, spaceID, filesystemID int, opts linodego.NFSSnapshotCreateOptions) (*linodego.NFSSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.filesystemLocked(spaceID, filesystemID); err != nil {
		return nil, err
	}
	if opts.Label == "" {
		return nil, fakeSanityInvalid("NFS snapshot label is required")
	}
	for snapshotID := range c.snapshots {
		if c.snapshots[snapshotID].Label == opts.Label {
			return nil, fakeSanityConflict("NFS snapshot %q already exists", opts.Label)
		}
	}
	key := opts.Label
	if _, ok := c.snapshotByName[key]; ok {
		return nil, fakeSanityConflict("NFS snapshot %q already exists", opts.Label)
	}
	now := time.Now().UTC()
	snapshot := linodego.NFSSnapshot{
		ID:           c.nextSnapshotID,
		FilesystemID: filesystemID,
		SpaceID:      spaceID,
		Label:        opts.Label,
		Status:       linodego.NFSSnapshotStatusCreating,
		Created:      &now,
		Locked:       opts.Locked != nil && *opts.Locked,
		Source:       linodego.NFSSnapshotSourceManual,
		SizeBytes:    0,
		Tags:         cloneSanityStrings(opts.Tags),
	}
	c.nextSnapshotID++
	c.snapshots[snapshot.ID] = snapshot
	c.snapshotByName[snapshot.Label] = snapshot.ID
	result := cloneSanitySnapshot(&snapshot)
	return &result, nil
}

func (c *fakeSanityLinodeClient) WaitForNFSSnapshotStatus(_ context.Context, spaceID, filesystemID, snapshotID int, status linodego.NFSSnapshotStatus) (*linodego.NFSSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot, err := c.snapshotLocked(spaceID, filesystemID, snapshotID)
	if err != nil {
		return nil, err
	}
	if status != linodego.NFSSnapshotStatusActive {
		return nil, fakeSanityInvalid("NFS snapshot %d is only available in active status", snapshotID)
	}
	snapshot.Status = linodego.NFSSnapshotStatusActive
	snapshot.SizeBytes = fakeSanityFilesystemCapacity
	c.snapshots[snapshotID] = snapshot
	result := cloneSanitySnapshot(&snapshot)
	return &result, nil
}

func (c *fakeSanityLinodeClient) DeleteNFSSnapshot(_ context.Context, spaceID, filesystemID, snapshotID int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot, err := c.snapshotLocked(spaceID, filesystemID, snapshotID)
	if err != nil {
		return err
	}
	if snapshot.Locked {
		return fakeSanityConflict("NFS snapshot %d is locked", snapshotID)
	}
	delete(c.snapshots, snapshotID)
	delete(c.snapshotByName, snapshot.Label)
	return nil
}

func (c *fakeSanityLinodeClient) CloneNFSSnapshot(_ context.Context, spaceID, filesystemID, snapshotID int, opts linodego.NFSSnapshotCloneOptions) (*linodego.NFSFilesystem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.snapshotLocked(spaceID, filesystemID, snapshotID); err != nil {
		return nil, err
	}
	if opts.Label == "" {
		return nil, fakeSanityInvalid("NFS filesystem label is required")
	}
	if opts.Region != sanityRegion {
		return nil, fakeSanityInvalid("NFS filesystem region must be %q", sanityRegion)
	}
	targetSpaceID := spaceID
	if opts.SpaceID != nil {
		if *opts.SpaceID == nil {
			return nil, fakeSanityInvalid("NFS filesystem clone space ID is invalid")
		}
		targetSpaceID = **opts.SpaceID
	}
	if err := c.requireSpaceLocked(targetSpaceID); err != nil {
		return nil, err
	}
	if opts.SizeGib != nil && (*opts.SizeGib == nil || **opts.SizeGib <= 0) {
		return nil, fakeSanityInvalid("NFS filesystem clone size must be positive")
	}
	name := fakeSanityFilesystemName(targetSpaceID, opts.Label)
	if filesystemID, ok := c.filesystemByName[name]; ok {
		filesystem := c.filesystems[filesystemID]
		if !sanitySnapshotCloneCompatible(&filesystem, snapshotID, opts) {
			return nil, fakeSanityConflict("NFS filesystem %q already exists with incompatible parameters", opts.Label)
		}
		result := cloneSanityFilesystem(&filesystem)
		return &result, nil
	}
	protocols := []linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4}
	filesystem := newSanityFilesystem(c.nextFilesystemID, targetSpaceID, linodego.NFSFilesystemCreateOptions{
		Label: opts.Label, Region: opts.Region, ProtocolVersions: &protocols, Tags: opts.Tags,
	}, &snapshotID)
	deactivateSanityFilesystem(&filesystem)
	c.nextFilesystemID++
	c.filesystems[filesystem.ID] = filesystem
	c.filesystemByName[name] = filesystem.ID
	c.filesystemPolicy[filesystem.ID] = newSanityFilesystemPolicy(&filesystem)
	result := cloneSanityFilesystem(&filesystem)
	return &result, nil
}

func (c *fakeSanityLinodeClient) requireSpaceLocked(spaceID int) error {
	if spaceID != c.space.ID {
		return fakeSanityNotFound("NFS space", spaceID)
	}
	return nil
}

func (c *fakeSanityLinodeClient) filesystemLocked(spaceID, filesystemID int) (linodego.NFSFilesystem, error) {
	if err := c.requireSpaceLocked(spaceID); err != nil {
		return linodego.NFSFilesystem{}, err
	}
	filesystem, ok := c.filesystems[filesystemID]
	if !ok {
		return linodego.NFSFilesystem{}, fakeSanityNotFound("NFS filesystem", filesystemID)
	}
	return filesystem, nil
}

func (c *fakeSanityLinodeClient) snapshotLocked(spaceID, filesystemID, snapshotID int) (linodego.NFSSnapshot, error) {
	if _, err := c.filesystemLocked(spaceID, filesystemID); err != nil {
		return linodego.NFSSnapshot{}, err
	}
	snapshot, ok := c.snapshots[snapshotID]
	if !ok || snapshot.SpaceID != spaceID || snapshot.FilesystemID != filesystemID {
		return linodego.NFSSnapshot{}, fakeSanityNotFound("NFS snapshot", snapshotID)
	}
	return snapshot, nil
}

func validateSanityFilesystemCreate(opts linodego.NFSFilesystemCreateOptions) error {
	if opts.Label == "" {
		return fakeSanityInvalid("NFS filesystem label is required")
	}
	if opts.Region != sanityRegion {
		return fakeSanityInvalid("NFS filesystem region must be %q", sanityRegion)
	}
	if opts.ProtocolVersions == nil || !slices.Equal(*opts.ProtocolVersions, []linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4}) {
		return fakeSanityInvalid("NFS filesystem protocol_versions must be [nfsv4]")
	}
	return nil
}

func validateSanitySpacePolicy(opts linodego.NFSSpaceAccessPolicyUpdateOptions) error {
	if opts.MTLSMode != nil {
		switch *opts.MTLSMode {
		case linodego.NFSMTLSModeDisabled, linodego.NFSMTLSModeOptional, linodego.NFSMTLSModeRequired:
		default:
			return fakeSanityInvalid("invalid NFS space mTLS mode %q", *opts.MTLSMode)
		}
	}
	if opts.VPCs == nil {
		return nil
	}
	seen := make(map[int]struct{}, len(*opts.VPCs))
	for i := range *opts.VPCs {
		vpc := &(*opts.VPCs)[i]
		if vpc.ID != sanityVPCID {
			return fakeSanityInvalid("VPC %d is unavailable", vpc.ID)
		}
		if _, duplicate := seen[vpc.ID]; duplicate {
			return fakeSanityInvalid("VPC %d is specified more than once", vpc.ID)
		}
		seen[vpc.ID] = struct{}{}
		for _, subnetID := range vpc.Subnets {
			if subnetID <= 0 {
				return fakeSanityInvalid("VPC subnet ID must be positive")
			}
		}
	}
	return nil
}

func validateSanityFilesystemPolicy(opts linodego.NFSFilesystemAccessPolicyUpdateOptions) error {
	if opts.LinodeIDs != nil {
		seen := make(map[int]struct{}, len(*opts.LinodeIDs))
		for _, linodeID := range *opts.LinodeIDs {
			if linodeID <= 0 {
				return fakeSanityInvalid("Linode ID must be positive")
			}
			if _, duplicate := seen[linodeID]; duplicate {
				return fakeSanityInvalid("Linode ID %d is specified more than once", linodeID)
			}
			seen[linodeID] = struct{}{}
		}
	}
	if opts.SquashPolicy != nil {
		switch *opts.SquashPolicy {
		case linodego.NFSSquashPolicyNone, linodego.NFSSquashPolicyRootSquash, linodego.NFSSquashPolicyAllSquash:
		default:
			return fakeSanityInvalid("invalid NFS filesystem squash policy %q", *opts.SquashPolicy)
		}
	}
	if opts.Protocols != nil && !slices.Equal(*opts.Protocols, []linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4}) {
		return fakeSanityInvalid("NFS filesystem access policy protocols must be [nfsv4]")
	}
	return nil
}

func newSanityFilesystem(id, spaceID int, opts linodego.NFSFilesystemCreateOptions, sourceSnapshotID *int) linodego.NFSFilesystem {
	now := time.Now().UTC()
	return linodego.NFSFilesystem{
		ID: id, SpaceID: spaceID, Label: opts.Label, Region: opts.Region,
		ProtocolVersions: append([]linodego.NFSProtocolVersion(nil), (*opts.ProtocolVersions)...),
		Status:           linodego.NFSFilesystemStatusActive,
		SourceSnapshotID: cloneSanityInt(sourceSnapshotID),
		Created:          &now,
		Updated:          &now,
		Tags:             cloneSanityStrings(opts.Tags),
	}
}

func activateSanityFilesystem(filesystem *linodego.NFSFilesystem) {
	now := sanityNow()
	mountPath := fmt.Sprintf("/%s-%x", filesystem.Label, filesystem.ID)
	mountTarget := fmt.Sprintf("sanity-space-%x.nfs.%s.linode.com:%s", filesystem.SpaceID, filesystem.Region, mountPath)
	usedCapacity := int64(0)
	maxCapacity := fakeSanityFilesystemCapacity
	filesystem.Status = linodego.NFSFilesystemStatusActive
	filesystem.MountTargetIPs = []string{fmt.Sprintf("[2001:db8::%x]:%s", filesystem.ID, mountPath)}
	filesystem.MountTargetFQDN = &mountTarget
	filesystem.Stats = linodego.NFSFilesystemStats{
		UsedCapacityBytes: &usedCapacity,
		MaxCapacityBytes:  &maxCapacity,
		CollectedAt:       now,
	}
	filesystem.Updated = now
}

func deactivateSanityFilesystem(filesystem *linodego.NFSFilesystem) {
	filesystem.Status = linodego.NFSFilesystemStatusCreating
	filesystem.MountTargetIPs = nil
	filesystem.MountTargetFQDN = nil
	filesystem.Stats = linodego.NFSFilesystemStats{}
}

func newSanityFilesystemPolicy(filesystem *linodego.NFSFilesystem) linodego.NFSFilesystemAccessPolicy {
	now := time.Now().UTC()
	return linodego.NFSFilesystemAccessPolicy{
		FilesystemID: filesystem.ID,
		Label:        filesystem.Label,
		SquashPolicy: linodego.NFSSquashPolicyRootSquash,
		Protocols:    []linodego.NFSProtocolVersion{linodego.NFSProtocolVersionV4},
		Status:       linodego.NFSAccessPolicyStatusActive,
		Created:      &now,
		Updated:      &now,
	}
}

func sanityFilesystemCreateCompatible(filesystem *linodego.NFSFilesystem, opts linodego.NFSFilesystemCreateOptions) bool {
	return filesystem.Label == opts.Label && filesystem.Region == opts.Region &&
		slices.Equal(filesystem.ProtocolVersions, *opts.ProtocolVersions) &&
		slices.Equal(filesystem.Tags, cloneSanityStrings(opts.Tags))
}

func sanitySnapshotCloneCompatible(filesystem *linodego.NFSFilesystem, snapshotID int, opts linodego.NFSSnapshotCloneOptions) bool {
	return filesystem.SourceSnapshotID != nil && *filesystem.SourceSnapshotID == snapshotID &&
		filesystem.Region == opts.Region && slices.Equal(filesystem.Tags, cloneSanityStrings(opts.Tags))
}

func fakeSanityFilesystemName(spaceID int, label string) string {
	return fmt.Sprintf("%d/%s", spaceID, label)
}

func fakeSanityNotFound(resource string, id int) *linodego.Error {
	return &linodego.Error{Code: http.StatusNotFound, Message: fmt.Sprintf("%s %d not found", resource, id)}
}

func fakeSanityConflict(format string, args ...any) *linodego.Error {
	return &linodego.Error{Code: http.StatusConflict, Message: fmt.Sprintf(format, args...)}
}

func fakeSanityInvalid(format string, args ...any) *linodego.Error {
	return &linodego.Error{Code: http.StatusBadRequest, Message: fmt.Sprintf(format, args...)}
}

func cloneSanitySpace(space *linodego.NFSSpace) linodego.NFSSpace {
	result := *space
	result.Description = cloneSanityString(space.Description)
	result.Created = cloneSanityTime(space.Created)
	result.Updated = cloneSanityTime(space.Updated)
	result.Tags = append([]string(nil), space.Tags...)
	return result
}

func cloneSanityFilesystem(filesystem *linodego.NFSFilesystem) linodego.NFSFilesystem {
	result := *filesystem
	result.ProtocolVersions = append([]linodego.NFSProtocolVersion(nil), filesystem.ProtocolVersions...)
	result.MountTargetIPs = append([]string(nil), filesystem.MountTargetIPs...)
	result.MountTargetFQDN = cloneSanityString(filesystem.MountTargetFQDN)
	result.SnapshotUsageBytes = cloneSanityInt64(filesystem.SnapshotUsageBytes)
	result.LDAPConfigID = cloneSanityString(filesystem.LDAPConfigID)
	result.SourceSnapshotID = cloneSanityInt(filesystem.SourceSnapshotID)
	result.Created = cloneSanityTime(filesystem.Created)
	result.Updated = cloneSanityTime(filesystem.Updated)
	result.Tags = append([]string(nil), filesystem.Tags...)
	result.Stats = linodego.NFSFilesystemStats{
		UsedCapacityBytes: cloneSanityInt64(filesystem.Stats.UsedCapacityBytes),
		MaxCapacityBytes:  cloneSanityInt64(filesystem.Stats.MaxCapacityBytes),
		CollectedAt:       cloneSanityTime(filesystem.Stats.CollectedAt),
	}
	return result
}

func cloneSanitySnapshot(snapshot *linodego.NFSSnapshot) linodego.NFSSnapshot {
	result := *snapshot
	result.Created = cloneSanityTime(snapshot.Created)
	result.Expiration = cloneSanityTime(snapshot.Expiration)
	result.PolicyID = cloneSanityInt(snapshot.PolicyID)
	result.Tags = append([]string(nil), snapshot.Tags...)
	return result
}

func cloneSanitySpacePolicy(policy *linodego.NFSSpaceAccessPolicy) linodego.NFSSpaceAccessPolicy {
	result := *policy
	result.VPCACL = make([]linodego.NFSSpaceAccessPolicyVPC, len(policy.VPCACL))
	for vpcIndex := range policy.VPCACL {
		vpc := &policy.VPCACL[vpcIndex]
		result.VPCACL[vpcIndex] = *vpc
		result.VPCACL[vpcIndex].Label = cloneSanityString(vpc.Label)
		result.VPCACL[vpcIndex].URL = cloneSanityString(vpc.URL)
		result.VPCACL[vpcIndex].Range = cloneSanityString(vpc.Range)
		result.VPCACL[vpcIndex].Subnets = append([]linodego.NFSSpaceAccessPolicyVPCSubnet(nil), vpc.Subnets...)
	}
	result.MTLSCACert = cloneSanityString(policy.MTLSCACert)
	result.Created = cloneSanityTime(policy.Created)
	result.Updated = cloneSanityTime(policy.Updated)
	return result
}

func cloneSanityFilesystemPolicy(policy *linodego.NFSFilesystemAccessPolicy) linodego.NFSFilesystemAccessPolicy {
	result := *policy
	result.LinodeACL = append([]linodego.NFSFilesystemAccessPolicyLinode(nil), policy.LinodeACL...)
	result.Protocols = append([]linodego.NFSProtocolVersion(nil), policy.Protocols...)
	result.Created = cloneSanityTime(policy.Created)
	result.Updated = cloneSanityTime(policy.Updated)
	return result
}

func cloneSanityStrings(values *[]string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), (*values)...)
}

func cloneSanityString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneSanityInt(value *int) *int {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneSanityInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func sanityNow() *time.Time {
	now := time.Now().UTC()
	return &now
}

func cloneSanityTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

type sanityListFilterFields struct {
	label  string
	region string
}

func sanityListFilter(opts *linodego.ListOptions) (sanityListFilterFields, error) {
	if opts == nil || opts.Filter == "" {
		return sanityListFilterFields{}, nil
	}

	var fields map[string]string
	if err := json.Unmarshal([]byte(opts.Filter), &fields); err != nil {
		return sanityListFilterFields{}, fakeSanityInvalid("invalid list filter: %v", err)
	}
	return sanityListFilterFields{
		label:  fields["label"],
		region: fields["region"],
	}, nil
}
