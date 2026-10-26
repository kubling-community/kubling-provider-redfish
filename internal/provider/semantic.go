package provider

import (
	"bytes"
	"context"
	_ "embed"

	providersdk "github.com/kubling-community/kubling-providers/sdk-go/provider"
	"google.golang.org/grpc/status"
)

const redfishSemanticFragmentVersion = "1.0.0"

//go:generate go run ../../cmd/redfish-semantic-gen --root ../..

//go:embed semantic/redfish-inventory-v1.yaml
var redfishSemanticFragmentDocument []byte

// SemanticFragment returns the generated Redfish inventory model without
// opening a logical connection or contacting any configured BMC.
func (*Provider) SemanticFragment(ctx context.Context) (*providersdk.SemanticFragment, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	return &providersdk.SemanticFragment{
		Document:  bytes.Clone(redfishSemanticFragmentDocument),
		MediaType: providersdk.SemanticFragmentMediaTypeYAML,
		Version:   redfishSemanticFragmentVersion,
	}, nil
}

var _ providersdk.SemanticFragmentProvider = (*Provider)(nil)
