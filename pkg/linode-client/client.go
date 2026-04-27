package linodeclient

import (
	"github.com/linode/linodego"
)

// NewLinodeClient mirrors the block storage driver setup and returns a
// configured linodego client.
func NewLinodeClient(token, userAgent, baseURL string) (*linodego.Client, error) {
	linodeClient := linodego.NewClient(nil)

	var (
		client *linodego.Client
		err    error
	)
	if baseURL != "" {
		client, err = linodeClient.UseURL(baseURL)
		if err != nil {
			return nil, err
		}
	} else {
		client = &linodeClient
	}

	client.SetUserAgent(userAgent)
	client.SetToken(token)

	return client, nil
}
