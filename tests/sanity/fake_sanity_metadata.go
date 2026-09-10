package sanity

import (
	"context"
	"fmt"
	"net/netip"

	metadataapi "github.com/linode/go-metadata"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/linode/linode-filestorage-csi-driver/internal/driver"
)

const (
	sanityNodeID         = 123
	sanityNodeProviderID = "linode://123"
	sanityRegion         = "us-east"
	sanityVPCID          = 456
)

type fakeSanityInstanceMetadataClient struct {
	instance    *metadataapi.InstanceData
	network     *metadataapi.NetworkData
	instanceErr error
	networkErr  error
}

var _ driver.InstanceMetadataClient = (*fakeSanityInstanceMetadataClient)(nil)

func newFakeSanityInstanceMetadataClient() *fakeSanityInstanceMetadataClient {
	return &fakeSanityInstanceMetadataClient{
		instance: &metadataapi.InstanceData{ID: sanityNodeID, Region: sanityRegion},
		network: &metadataapi.NetworkData{
			IPv4: metadataapi.IPv4Data{
				Private: []netip.Prefix{netip.MustParsePrefix("192.0.2.123/32")},
			},
		},
	}
}

func (c *fakeSanityInstanceMetadataClient) GetInstance(context.Context) (*metadataapi.InstanceData, error) {
	if c.instanceErr != nil {
		return nil, c.instanceErr
	}
	return c.instance, nil
}

func (c *fakeSanityInstanceMetadataClient) GetNetwork(context.Context) (*metadataapi.NetworkData, error) {
	if c.networkErr != nil {
		return nil, c.networkErr
	}
	return c.network, nil
}

type fakeSanityKubeNodeClient struct {
	nodes      *corev1.NodeList
	getNodeErr error
	listErr    error
}

var _ driver.KubeNodeClient = (*fakeSanityKubeNodeClient)(nil)

func newFakeSanityKubeNodeClient() *fakeSanityKubeNodeClient {
	return &fakeSanityKubeNodeClient{
		nodes: &corev1.NodeList{Items: []corev1.Node{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "sanity-node",
				Labels: map[string]string{
					"topology.kubernetes.io/region": sanityRegion,
				},
			},
			Spec: corev1.NodeSpec{ProviderID: sanityNodeProviderID},
		}}},
	}
}

func (c *fakeSanityKubeNodeClient) GetNode(_ context.Context, name string) (*corev1.Node, error) {
	if c.getNodeErr != nil {
		return nil, c.getNodeErr
	}
	for i := range c.nodes.Items {
		if c.nodes.Items[i].Name == name {
			return c.nodes.Items[i].DeepCopy(), nil
		}
	}
	return nil, fmt.Errorf("node %q not found", name)
}

func (c *fakeSanityKubeNodeClient) ListNodes(context.Context) (*corev1.NodeList, error) {
	if c.listErr != nil {
		return nil, c.listErr
	}
	return c.nodes.DeepCopy(), nil
}
