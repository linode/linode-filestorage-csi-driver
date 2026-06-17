package linodeclient

import (
	"context"

	"github.com/linode/linodego/v2"
)

type LinodeClient interface {
	ListInterfaces(ctx context.Context, linodeID int, opts *linodego.ListOptions) ([]linodego.LinodeInterface, error)
	ListNFSSpaces(ctx context.Context, opts *linodego.ListOptions) ([]linodego.NFSSpace, error)
	GetNFSSpace(ctx context.Context, spaceID string) (*linodego.NFSSpace, error)
	ListNFSFilesystems(ctx context.Context, spaceID string, opts *linodego.ListOptions) ([]linodego.NFSFilesystem, error)
	GetNFSFilesystem(ctx context.Context, spaceID string, filesystemID string) (*linodego.NFSFilesystem, error)
	GetNFSFilesystemByID(ctx context.Context, filesystemID string) (*linodego.NFSFilesystem, error)
	CreateNFSFilesystem(ctx context.Context, spaceID string, opts linodego.NFSFilesystemCreateOptions) (*linodego.NFSFilesystem, error)
	DeleteNFSFilesystem(ctx context.Context, spaceID string, filesystemID string) error
	GetNFSSpaceAccessPolicy(ctx context.Context, spaceID string) (*linodego.NFSSpaceAccessPolicy, error)
	UpdateNFSSpaceAccessPolicy(ctx context.Context, spaceID string, opts linodego.NFSSpaceAccessPolicyUpdateOptions) (*linodego.NFSSpaceAccessPolicy, error)
	GetNFSFilesystemAccessPolicy(ctx context.Context, spaceID string, filesystemID string) (*linodego.NFSFilesystemAccessPolicy, error)
	UpdateNFSFilesystemAccessPolicy(ctx context.Context, spaceID string, filesystemID string, opts linodego.NFSFilesystemAccessPolicyUpdateOptions) (*linodego.NFSFilesystemAccessPolicy, error)
}

var _ LinodeClient = (*linodego.Client)(nil)

// NewLinodeClient mirrors the block storage driver setup and returns a
// configured linodego client.
func NewLinodeClient(token, userAgent, baseURL string) (*linodego.Client, error) {
	linodeClient, err := linodego.NewClient(nil)
	if err != nil {
		return nil, err
	}

	client := &linodeClient
	if baseURL != "" {
		client, err = linodeClient.UseURL(baseURL)
		if err != nil {
			return nil, err
		}
	}

	client.SetUserAgent(userAgent)
	client.SetToken(token)

	return client, nil
}
