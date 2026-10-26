// Package query plans and streams bounded read-only Redfish queries.
package query

import (
	"fmt"
	"net/url"
	"strings"

	kublingv1 "github.com/kubling-community/kubling-grpc/sdk-go/kubling/v1"
	"github.com/kubling-community/kubling-provider-redfish/internal/catalog"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	"google.golang.org/protobuf/proto"
)

type plannedProjection struct {
	column catalog.Column
	field  *providerv1.Field
}

type accessRoute struct {
	namespace   *string
	deviceID    *string
	resourceURI *string
	impossible  bool
}

func (r accessRoute) unbounded() bool {
	return r.namespace == nil && r.deviceID == nil
}

type queryPlan struct {
	table       catalog.Table
	projections []plannedProjection
	route       accessRoute
}

func (p queryPlan) fields() []*providerv1.Field {
	fields := make([]*providerv1.Field, 0, len(p.projections))
	for _, projection := range p.projections {
		fields = append(fields, proto.Clone(projection.field).(*providerv1.Field))
	}
	return fields
}

func buildPlan(request *providerv1.QueryRequest) (queryPlan, error) {
	if request == nil {
		return queryPlan{}, fmt.Errorf("query request is required")
	}
	entity := request.GetEntity()
	if entity == nil || strings.TrimSpace(entity.GetName()) == "" {
		return queryPlan{}, fmt.Errorf("query entity is required")
	}
	if entity.GetNamespace() != catalog.Namespace {
		return queryPlan{}, fmt.Errorf("query entity namespace must be %q", catalog.Namespace)
	}
	if len(request.GetOrderBy()) != 0 || request.Limit != nil || request.Offset != nil ||
		len(request.GetGroupBy()) != 0 || request.GetHaving() != nil {
		return queryPlan{}, fmt.Errorf("ordering, relational pagination and aggregation are not supported")
	}

	table, exists := catalog.LookupTable(entity.GetName())
	if !exists {
		return queryPlan{}, fmt.Errorf("unknown Redfish table %q", entity.GetName())
	}
	columns := make(map[string]catalog.Column, len(table.Columns))
	for _, column := range table.Columns {
		columns[strings.ToLower(column.Name)] = column
	}

	requested := request.GetProjections()
	projections := make([]plannedProjection, 0, max(len(requested), len(table.Columns)))
	if len(requested) == 0 {
		for _, column := range table.Columns {
			projections = append(projections, newProjection(column, column.Name))
		}
	} else {
		for index, projection := range requested {
			if projection == nil || projection.GetExpression() == nil {
				return queryPlan{}, fmt.Errorf("projection %d is required", index)
			}
			field := projection.GetExpression().GetField()
			if field == nil || strings.TrimSpace(field.GetName()) == "" {
				return queryPlan{}, fmt.Errorf("projection %d must be a field reference", index)
			}
			column, exists := columns[strings.ToLower(strings.TrimSpace(field.GetName()))]
			if !exists {
				return queryPlan{}, fmt.Errorf("unknown %s column %q", table.Name, field.GetName())
			}
			outputName := strings.TrimSpace(projection.GetOutputName())
			if outputName == "" {
				outputName = column.Name
			}
			projections = append(projections, newProjection(column, outputName))
		}
	}

	route, err := parseAccessFilter(request.GetFilter())
	if err != nil {
		return queryPlan{}, err
	}
	for _, access := range []struct {
		name  string
		value *string
	}{
		{name: "namespace", value: route.namespace},
		{name: "device_id", value: route.deviceID},
		{name: "resource_uri", value: route.resourceURI},
	} {
		if access.value == nil {
			continue
		}
		column, exists := columns[access.name]
		if !exists || !column.EqualitySearch {
			return queryPlan{}, fmt.Errorf("table %s does not support %s access", table.Name, access.name)
		}
	}
	if route.resourceURI != nil {
		parsed, _ := url.Parse(*route.resourceURI)
		if table.Discovery.Kind != catalog.DiscoveryGraph && parsed.Fragment != "" {
			return queryPlan{}, fmt.Errorf("table %s does not support embedded resource_uri access", table.Name)
		}
	}
	return queryPlan{table: table, projections: projections, route: route}, nil
}

func newProjection(column catalog.Column, outputName string) plannedProjection {
	return plannedProjection{
		column: column,
		field: &providerv1.Field{
			Name: outputName,
			Type: column.Type,
			TypeDescriptor: &kublingv1.TypeDescriptor{
				Type: column.Type,
			},
		},
	}
}

func parseAccessFilter(expression *providerv1.Expression) (accessRoute, error) {
	if expression == nil {
		return accessRoute{}, nil
	}
	switch kind := expression.GetKind().(type) {
	case *providerv1.Expression_Comparison:
		if kind.Comparison == nil ||
			kind.Comparison.GetOperator() != providerv1.ComparisonOperator_COMPARISON_OPERATOR_EQUAL {
			return accessRoute{}, fmt.Errorf("only equality access filters are supported")
		}
		field, value, ok := fieldStringEquality(kind.Comparison.GetLeft(), kind.Comparison.GetRight())
		if !ok {
			field, value, ok = fieldStringEquality(kind.Comparison.GetRight(), kind.Comparison.GetLeft())
		}
		if !ok {
			return accessRoute{}, fmt.Errorf("access filter must compare a field with a string literal")
		}
		route := accessRoute{}
		switch strings.ToLower(strings.TrimSpace(field)) {
		case "namespace":
			route.namespace = &value
		case "device_id":
			route.deviceID = &value
		case "resource_uri":
			if err := validateResourceURI(value); err != nil {
				return accessRoute{}, err
			}
			route.resourceURI = &value
		default:
			return accessRoute{}, fmt.Errorf("column %q is not an access field", field)
		}
		return route, nil
	case *providerv1.Expression_Logical:
		if kind.Logical == nil ||
			kind.Logical.GetOperator() != providerv1.LogicalOperator_LOGICAL_OPERATOR_AND ||
			len(kind.Logical.GetOperands()) < 2 {
			return accessRoute{}, fmt.Errorf("only AND combinations of access equalities are supported")
		}
		route := accessRoute{}
		for _, operand := range kind.Logical.GetOperands() {
			part, err := parseAccessFilter(operand)
			if err != nil {
				return accessRoute{}, err
			}
			mergeAccessValue(&route.namespace, part.namespace, &route.impossible)
			mergeAccessValue(&route.deviceID, part.deviceID, &route.impossible)
			mergeAccessValue(&route.resourceURI, part.resourceURI, &route.impossible)
		}
		return route, nil
	default:
		return accessRoute{}, fmt.Errorf("unsupported access filter expression")
	}
}

func validateResourceURI(value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("resource_uri access value must be a canonical Redfish path")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil ||
		parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery ||
		!strings.HasPrefix(parsed.EscapedPath(), "/redfish/v1/") ||
		(parsed.Fragment != "" && !strings.HasPrefix(parsed.Fragment, "/")) {
		return fmt.Errorf("resource_uri access value must be a canonical Redfish path")
	}
	return nil
}

func fieldStringEquality(
	fieldExpression *providerv1.Expression,
	literalExpression *providerv1.Expression,
) (string, string, bool) {
	if fieldExpression == nil || literalExpression == nil {
		return "", "", false
	}
	field := fieldExpression.GetField()
	literal := literalExpression.GetLiteral()
	if field == nil || literal == nil || literal.GetValue() == nil {
		return "", "", false
	}
	value, ok := literal.GetValue().GetKind().(*kublingv1.Value_StringValue)
	if !ok {
		return "", "", false
	}
	return field.GetName(), value.StringValue, true
}

func mergeAccessValue(destination **string, candidate *string, impossible *bool) {
	if candidate == nil || *impossible {
		return
	}
	if *destination != nil && **destination != *candidate {
		*impossible = true
		return
	}
	value := *candidate
	*destination = &value
}
