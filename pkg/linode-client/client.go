package linodeclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/linode/linodego/v2"
)

const DefaultListPageSize = 500

type LinodeClient interface {
	GetInstance(ctx context.Context, linodeID int) (*linodego.Instance, error)
	ListInterfaces(ctx context.Context, linodeID int, opts *linodego.ListOptions) ([]linodego.LinodeInterface, error)
	ListInstanceConfigs(ctx context.Context, linodeID int, opts *linodego.ListOptions) ([]linodego.InstanceConfig, error)

	ListNFSSpaces(ctx context.Context, opts *linodego.ListOptions) ([]linodego.NFSSpace, error)
	GetNFSSpace(ctx context.Context, spaceID int) (*linodego.NFSSpace, error)

	ListNFSFilesystems(ctx context.Context, spaceID int, opts *linodego.ListOptions) ([]linodego.NFSFilesystem, error)
	GetNFSFilesystem(ctx context.Context, spaceID int, filesystemID int) (*linodego.NFSFilesystem, error)
	CreateNFSFilesystem(ctx context.Context, spaceID int, opts linodego.NFSFilesystemCreateOptions) (*linodego.NFSFilesystem, error)
	WaitForNFSFilesystemStatus(ctx context.Context, spaceID int, filesystemID int, status linodego.NFSFilesystemStatus) (*linodego.NFSFilesystem, error)
	DeleteNFSFilesystem(ctx context.Context, spaceID int, filesystemID int) error

	GetNFSSpaceAccessPolicy(ctx context.Context, spaceID int) (*linodego.NFSSpaceAccessPolicy, error)
	UpdateNFSSpaceAccessPolicy(ctx context.Context, spaceID int, opts linodego.NFSSpaceAccessPolicyUpdateOptions) (*linodego.NFSSpaceAccessPolicy, error)
	WaitForNFSSpaceAccessPolicyStatus(ctx context.Context, spaceID int, status linodego.NFSAccessPolicyStatus) (*linodego.NFSSpaceAccessPolicy, error)
	GetNFSFilesystemAccessPolicy(ctx context.Context, spaceID int, filesystemID int) (*linodego.NFSFilesystemAccessPolicy, error)
	UpdateNFSFilesystemAccessPolicy(ctx context.Context, spaceID int, filesystemID int, opts linodego.NFSFilesystemAccessPolicyUpdateOptions) (*linodego.NFSFilesystemAccessPolicy, error)
	WaitForNFSFilesystemAccessPolicyStatus(ctx context.Context, spaceID int, filesystemID int, status linodego.NFSAccessPolicyStatus) (*linodego.NFSFilesystemAccessPolicy, error)

	ListNFSSnapshots(ctx context.Context, spaceID int, filesystemID int, opts *linodego.ListOptions) ([]linodego.NFSSnapshot, error)
	GetNFSSnapshot(ctx context.Context, spaceID int, filesystemID int, snapshotID int) (*linodego.NFSSnapshot, error)
	CreateNFSSnapshot(ctx context.Context, spaceID int, filesystemID int, opts linodego.NFSSnapshotCreateOptions) (*linodego.NFSSnapshot, error)
	WaitForNFSSnapshotStatus(ctx context.Context, spaceID int, filesystemID int, snapshotID int, status linodego.NFSSnapshotStatus) (*linodego.NFSSnapshot, error)
	DeleteNFSSnapshot(ctx context.Context, spaceID int, filesystemID int, snapshotID int) error
	CloneNFSSnapshot(ctx context.Context, spaceID int, filesystemID int, snapshotID int, opts linodego.NFSSnapshotCloneOptions) (*linodego.NFSFilesystem, error)
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
	UserAgent           string
	DriverVersion       string
	RootCertificatePath string
	Timeout             time.Duration

	DriverRole       string
	AccessPolicyMode string
	CSIEndpoint      string
	NodeName         string
}

const defaultUserAgentProduct = "LinodeFileStorageCSI"

func (config *Config) userAgent() (string, error) {
	if config.DriverVersion == "" {
		return "", errors.New("driver version cannot be empty")
	}

	if config.UserAgent == "" {
		return fmt.Sprintf("%s/%s", defaultUserAgentProduct, config.DriverVersion), nil
	}

	if !strings.Contains(config.UserAgent, config.DriverVersion) {
		return "", fmt.Errorf("user agent %q must include driver version %q", config.UserAgent, config.DriverVersion)
	}

	return config.UserAgent, nil
}

// NewLinodeClient mirrors the block storage driver setup and returns a
// configured linodego client.
func NewLinodeClient(config *Config, opts ...Option) (*linodego.Client, error) {
	if config == nil {
		return nil, errors.New("config cannot be nil")
	}

	if config.LinodeToken == "" {
		return nil, errors.New("token cannot be empty")
	}

	userAgent, err := config.userAgent()
	if err != nil {
		return nil, err
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

	newClient.SetUserAgent(userAgent)

	for _, opt := range opts {
		opt.set(&newClient)
	}

	return &newClient, nil
}
