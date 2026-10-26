package provider

import (
	"context"
	"sort"

	kublingv1 "github.com/kubling-community/kubling-grpc/sdk-go/kubling/v1"
	"github.com/kubling-community/kubling-provider-redfish/internal/catalog"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	providersdk "github.com/kubling-community/kubling-providers/sdk-go/provider"
	"google.golang.org/grpc/status"
)

// Capabilities reports only behavior implemented by the current provider.
func (p *Provider) Capabilities(ctx context.Context) (*providersdk.Capabilities, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	return &providersdk.Capabilities{
		Transactions: &providerv1.TransactionCapabilities{Supported: false},
		Query: &providerv1.QueryCapabilities{
			PartialResults: p.config.AllowPartialResults,
			Ordering:       &providerv1.OrderingCapabilities{Supported: false},
			Pagination:     &providerv1.PaginationCapabilities{},
			Expressions: &providerv1.ExpressionCapabilities{
				ComparisonOperators: []providerv1.ComparisonOperator{
					providerv1.ComparisonOperator_COMPARISON_OPERATOR_EQUAL,
				},
				LogicalOperators: []providerv1.LogicalOperator{
					providerv1.LogicalOperator_LOGICAL_OPERATOR_AND,
				},
			},
		},
		Mutations: &providerv1.MutationCapabilities{},
		Values:    catalogValueCapabilities(),
	}, nil
}

func catalogValueCapabilities() *providerv1.ValueCapabilities {
	types := make(map[kublingv1.ValueType]struct{})
	for _, table := range catalog.Tables() {
		for _, column := range table.Columns {
			types[column.Type] = struct{}{}
		}
	}
	ordered := make([]kublingv1.ValueType, 0, len(types))
	for valueType := range types {
		ordered = append(ordered, valueType)
	}
	sort.Slice(ordered, func(left int, right int) bool { return ordered[left] < ordered[right] })
	supported := make([]*providerv1.SupportedValueType, 0, len(ordered))
	for _, valueType := range ordered {
		supported = append(supported, &providerv1.SupportedValueType{
			Type:   valueType,
			Input:  valueType == kublingv1.ValueType_VALUE_TYPE_STRING,
			Output: true,
		})
	}
	return &providerv1.ValueCapabilities{SupportedTypes: supported}
}
