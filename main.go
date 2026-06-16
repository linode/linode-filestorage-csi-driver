package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/linode/linodego/v2"
	"go.uber.org/automaxprocs/maxprocs"
	"k8s.io/klog/v2"

	"github.com/linode/linode-filestorage-csi-driver/internal/driver"
	linodeclient "github.com/linode/linode-filestorage-csi-driver/pkg/linode-client"
)

var vendorVersion string

type configuration struct {
	// csiEndpoint is the plugin socket the process listens on.
	//
	// Local development can use the default value, but Kubernetes manifests should
	// override this per role. The controller and node plugins should not reuse the
	// same host-mounted socket path when multiple CSI drivers are deployed on a
	// node, otherwise socket and registration paths can contend with each other.
	csiEndpoint string
	linodeToken string
	driverRole  string
	linodeURL   string
	nodeName    string
}

func loadConfig() configuration {
	return configuration{
		csiEndpoint: envOrDefault("CSI_ENDPOINT", "unix:///csi/csi.sock"),
		linodeToken: os.Getenv("LINODE_TOKEN"),
		driverRole:  envOrDefault("DRIVER_ROLE", string(driver.RoleController)),
		linodeURL:   envOrDefault("LINODE_URL", fmt.Sprintf("%s://%s", linodego.APIProto, linodego.APIHost)),
		nodeName:    os.Getenv("NODE_NAME"),
	}
}

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
	if vendorVersion == "" {
		vendorVersion = "dev"
	}

	cfg := loadConfig()
	if cfg.driverRole == string(driver.RoleController) && cfg.linodeToken == "" {
		return errors.New("linode token required for controller role")
	}

	linodeDriver := driver.GetLinodeDriver(ctx)
	uaPrefix := fmt.Sprintf("LinodeFileStorageCSI/%s", vendorVersion)
	client, err := linodeclient.NewLinodeClient(cfg.linodeToken, uaPrefix, cfg.linodeURL)
	if err != nil {
		return fmt.Errorf("create linode client: %w", err)
	}

	if err := linodeDriver.SetupLinodeDriver(
		ctx,
		client,
		driver.Name,
		vendorVersion,
		driver.Role(cfg.driverRole),
		cfg.nodeName,
	); err != nil {
		return fmt.Errorf("setup driver: %w", err)
	}

	klog.V(2).InfoS("starting driver", "role", cfg.driverRole, "endpoint", cfg.csiEndpoint)
	linodeDriver.Run(ctx, cfg.csiEndpoint)
	return nil
}
