package linodeclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"time"

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

	ListNFSSnapshots(ctx context.Context, spaceID string, filesystemID string, opts *linodego.ListOptions) ([]linodego.NFSSnapshot, error)
	GetNFSSnapshot(ctx context.Context, spaceID string, filesystemID string, snapshotID string) (*linodego.NFSSnapshot, error)
	CreateNFSSnapshot(ctx context.Context, spaceID string, filesystemID string, opts linodego.NFSSnapshotCreateOptions) (*linodego.NFSSnapshot, error)
	DeleteNFSSnapshot(ctx context.Context, spaceID string, filesystemID string, snapshotID string) error
	CloneNFSSnapshot(ctx context.Context, spaceID string, filesystemID string, snapshotID string, opts linodego.NFSSnapshotCloneOptions) (*linodego.NFSFilesystem, error)
}

var _ LinodeClient = (*linodego.Client)(nil)

type Option struct {
	set func(client *linodego.Client)
}

type Config struct {
	// csiEndpoint is the plugin socket the process listens on.
	//
	// Local development can use the default value, but Kubernetes manifests should
	// override this per role. The controller and node plugins should not reuse the
	// same host-mounted socket path when multiple CSI drivers are deployed on a
	// node, otherwise socket and registration paths can contend with each other.
	LinodeToken         string
	BaseURL             string
	UserAgent           string
	RootCertificatePath string
	Timeout             time.Duration

	DriverRole  string
	CSIEndpoint string
	NodeName    string
}

// NewLinodeClient mirrors the block storage driver setup and returns a
// configured linodego client.
func NewLinodeClient(config *Config, opts ...Option) (*linodego.Client, error) {
	if config.LinodeToken == "" {
		return nil, errors.New("token cannot be empty")
	}

	// Use system cert pool if root CA cert was not provided explicitly for this client.
	// Works around linodego not using system certs if LINODE_CA is provided,
	// which affects all clients spawned via linodego.NewClient.
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if config.RootCertificatePath == "" {
		systemCertPool, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("failed to load system cert pool: %w", err)
		}
		tlsConfig.RootCAs = systemCertPool
	}

	httpClient := &http.Client{
		Timeout: config.Timeout,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	newClient, err := linodego.NewClient(httpClient)
	if err != nil {
		return nil, err
	}

	newClient.SetToken(config.LinodeToken)
	if config.RootCertificatePath != "" {
		err := newClient.SetRootCertificate(config.RootCertificatePath)
		if err != nil {
			return nil, err
		}
	}

	if config.BaseURL != "" {
		_, err := newClient.UseURL(config.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("failed to set base URL: %w", err)
		}
	}

	newClient.SetUserAgent(config.UserAgent)

	for _, opt := range opts {
		opt.set(&newClient)
	}

	return &newClient, nil
}
