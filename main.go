package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"go.uber.org/automaxprocs/maxprocs"
	"k8s.io/klog/v2"

	"github.com/linode/linode-filestorage-csi-driver/internal/driver"
	linodeclient "github.com/linode/linode-filestorage-csi-driver/pkg/linode-client"
	mountmanager "github.com/linode/linode-filestorage-csi-driver/pkg/mount-manager"
)

const (
	defaultClientTimeout = time.Second * 10
)

var vendorVersion string

func loadConfig() linodeclient.Config {
	timeout, err := time.ParseDuration(envOrDefault("TIMEOUT", "10s"))
	if err != nil {
		klog.Warningf("invalid TIMEOUT value, using default: %v", defaultClientTimeout)
		timeout = defaultClientTimeout
	}

	// version is overridden by build time flags
	if vendorVersion == "" {
		vendorVersion = "dev"
	}

	return linodeclient.Config{
		LinodeToken:         os.Getenv("LINODE_TOKEN"),
		DriverVersion:       vendorVersion,
		RootCertificatePath: os.Getenv("LINODE_CA"),
		Timeout:             timeout,

		DriverRole:  envOrDefault("DRIVER_ROLE", string(driver.RoleController)),
		CSIEndpoint: envOrDefault("CSI_ENDPOINT", "unix:///csi/csi.sock"),
		NodeName:    os.Getenv("NODE_NAME"),
	}
}

// unfortunately there's no built-in function to default env vars, so we have to write our own
func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func main() {
	ctx := context.Background()

	klog.InitFlags(nil)
	if err := flag.Set("logtostderr", "true"); err != nil {
		klog.ErrorS(err, "failed to configure klog")
		os.Exit(1)
	}
	flag.Parse()
	maxProcs()

	if err := handle(ctx); err != nil {
		klog.ErrorS(err, "fatal error")
		os.Exit(1)
	}
}

func maxProcs() {
	_, err := maxprocs.Set(maxprocs.Logger(func(msg string, keysAndValues ...interface{}) {
		klog.V(2).Infof(msg, keysAndValues...)
	}))
	if err != nil {
		klog.ErrorS(err, "failed to set GOMAXPROCS")
	}
}

func handle(ctx context.Context) error {
	cfg := loadConfig()
	linodeDriver := driver.GetLinodeDriver(ctx)
	role, client, mounter, err := dependenciesForRole(&cfg)
	if err != nil {
		return err
	}

	if err := linodeDriver.SetupLinodeDriver(
		ctx,
		client,
		mounter,
		driver.Name,
		vendorVersion,
		role,
		cfg.NodeName,
	); err != nil {
		return fmt.Errorf("setup driver: %w", err)
	}

	klog.V(2).InfoS("starting driver", "role", cfg.DriverRole, "endpoint", cfg.CSIEndpoint)
	linodeDriver.Run(ctx, cfg.CSIEndpoint)
	return nil
}

func dependenciesForRole(cfg *linodeclient.Config) (driver.Role, linodeclient.LinodeClient, *mountmanager.SafeFormatAndMount, error) {
	role := driver.Role(cfg.DriverRole)
	switch role {
	case driver.RoleController:
		if cfg.LinodeToken == "" {
			return "", nil, nil, driver.ErrTokenRequired
		}

		client, err := linodeclient.NewLinodeClient(cfg)
		if err != nil {
			return "", nil, nil, fmt.Errorf("create linode client: %w", err)
		}

		return role, client, nil, nil
	case driver.RoleNode:
		return role, nil, mountmanager.NewSafeMounter(), nil
	default:
		return "", nil, nil, fmt.Errorf("invalid driver role %q", cfg.DriverRole)
	}
}
