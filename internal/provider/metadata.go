package provider

import (
	"context"

	"github.com/kubling-community/kubling-provider-redfish/internal/catalog"
	providersdk "github.com/kubling-community/kubling-providers/sdk-go/provider"
	"google.golang.org/grpc/status"
)

// Metadata returns the generated Redfish catalog without contacting a BMC.
func (p *Provider) Metadata(ctx context.Context) (*providersdk.Metadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	return catalog.Metadata(catalog.MetadataOptions{
		AllowUnboundedFanout: p.config.AllowUnboundedFanout,
	}), nil
}

var _ providersdk.MetadataProvider = (*Provider)(nil)
