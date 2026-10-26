package query

import (
	"reflect"
	"strings"
	"testing"

	kublingv1 "github.com/kubling-community/kubling-grpc/sdk-go/kubling/v1"
	"github.com/kubling-community/kubling-provider-redfish/internal/catalog"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
)

func TestBuildPlanResolvesProjectionAliasesAndIdentityRoute(t *testing.T) {
	plan, err := buildPlan(&providerv1.QueryRequest{
		Entity: &providerv1.EntityReference{Name: "systems", Namespace: catalog.Namespace},
		Projections: []*providerv1.Projection{
			fieldProjection("model", "hardware_model"),
			fieldProjection("observed_at", ""),
		},
		Filter: and(
			equality("namespace", "dc-a"),
			equality("device_id", "bmc-01"),
			equality("resource_uri", "/redfish/v1/Systems/1"),
		),
	})
	if err != nil {
		t.Fatalf("buildPlan() error = %v", err)
	}
	if plan.table.Name != "SYSTEMS" || len(plan.projections) != 2 {
		t.Fatalf("plan = %#v", plan)
	}
	if got := []string{plan.projections[0].column.Name, plan.projections[0].field.GetName(), plan.projections[1].field.GetName()}; !reflect.DeepEqual(got, []string{"model", "hardware_model", "observed_at"}) {
		t.Fatalf("projections = %v", got)
	}
	if plan.route.namespace == nil || *plan.route.namespace != "dc-a" ||
		plan.route.deviceID == nil || *plan.route.deviceID != "bmc-01" ||
		plan.route.resourceURI == nil || *plan.route.resourceURI != "/redfish/v1/Systems/1" ||
		plan.route.impossible || plan.route.unbounded() {
		t.Fatalf("route = %#v", plan.route)
	}
}

func TestBuildPlanDefaultsToEveryProviderColumn(t *testing.T) {
	plan, err := buildPlan(&providerv1.QueryRequest{
		Entity: &providerv1.EntityReference{Name: "DEVICES", Namespace: catalog.Namespace},
	})
	if err != nil {
		t.Fatalf("buildPlan() error = %v", err)
	}
	if len(plan.projections) != len(plan.table.Columns) || len(plan.projections) == 0 {
		t.Fatalf("projection count = %d, columns = %d", len(plan.projections), len(plan.table.Columns))
	}
	for index, projection := range plan.projections {
		if projection.field.GetName() != plan.table.Columns[index].Name ||
			projection.field.GetType() != plan.table.Columns[index].Type {
			t.Fatalf("projection %d = %v", index, projection)
		}
	}
}

func TestBuildPlanRecognizesContradictoryAccessCriteria(t *testing.T) {
	plan, err := buildPlan(&providerv1.QueryRequest{
		Entity: &providerv1.EntityReference{Name: "SYSTEMS", Namespace: catalog.Namespace},
		Filter: and(
			equality("device_id", "bmc-01"),
			equality("device_id", "bmc-02"),
		),
	})
	if err != nil {
		t.Fatalf("buildPlan() error = %v", err)
	}
	if !plan.route.impossible {
		t.Fatalf("route = %#v, want impossible", plan.route)
	}
}

func TestBuildPlanRejectsUnsupportedQuerySurface(t *testing.T) {
	limit := uint64(1)
	tests := map[string]*providerv1.QueryRequest{
		"missing entity": {},
		"wrong namespace": {
			Entity: &providerv1.EntityReference{Name: "SYSTEMS", Namespace: "other"},
		},
		"unknown table": {
			Entity: &providerv1.EntityReference{Name: "UNKNOWN", Namespace: catalog.Namespace},
		},
		"unknown projection": {
			Entity:      &providerv1.EntityReference{Name: "SYSTEMS", Namespace: catalog.Namespace},
			Projections: []*providerv1.Projection{fieldProjection("missing", "")},
		},
		"literal projection": {
			Entity:      &providerv1.EntityReference{Name: "SYSTEMS", Namespace: catalog.Namespace},
			Projections: []*providerv1.Projection{{Expression: stringLiteral("value")}},
		},
		"relational limit": {
			Entity: &providerv1.EntityReference{Name: "SYSTEMS", Namespace: catalog.Namespace},
			Limit:  &limit,
		},
		"non-access filter": {
			Entity: &providerv1.EntityReference{Name: "SYSTEMS", Namespace: catalog.Namespace},
			Filter: equality("model", "synthetic"),
		},
		"registry device filter": {
			Entity: &providerv1.EntityReference{Name: "NAMESPACES", Namespace: catalog.Namespace},
			Filter: equality("device_id", "bmc-01"),
		},
		"non-string access value": {
			Entity: &providerv1.EntityReference{Name: "SYSTEMS", Namespace: catalog.Namespace},
			Filter: &providerv1.Expression{Kind: &providerv1.Expression_Comparison{Comparison: &providerv1.ComparisonExpression{
				Operator: providerv1.ComparisonOperator_COMPARISON_OPERATOR_EQUAL,
				Left:     fieldExpression("device_id"),
				Right: &providerv1.Expression{Kind: &providerv1.Expression_Literal{Literal: &providerv1.Literal{
					Value: &kublingv1.Value{Kind: &kublingv1.Value_LongValue{LongValue: 1}},
				}}},
			}}},
		},
		"absolute resource URI": {
			Entity: &providerv1.EntityReference{Name: "SYSTEMS", Namespace: catalog.Namespace},
			Filter: equality("resource_uri", "https://bmc.example.invalid/redfish/v1/Systems/1"),
		},
		"resource URI with query": {
			Entity: &providerv1.EntityReference{Name: "SYSTEMS", Namespace: catalog.Namespace},
			Filter: equality("resource_uri", "/redfish/v1/Systems/1?secret=canary"),
		},
	}
	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := buildPlan(request); err == nil || strings.TrimSpace(err.Error()) == "" {
				t.Fatalf("buildPlan() error = %v", err)
			}
		})
	}
}

func fieldProjection(name string, outputName string) *providerv1.Projection {
	return &providerv1.Projection{Expression: fieldExpression(name), OutputName: outputName}
}

func fieldExpression(name string) *providerv1.Expression {
	return &providerv1.Expression{Kind: &providerv1.Expression_Field{Field: &providerv1.FieldReference{Name: name}}}
}

func stringLiteral(value string) *providerv1.Expression {
	return &providerv1.Expression{Kind: &providerv1.Expression_Literal{Literal: &providerv1.Literal{
		Value: &kublingv1.Value{Kind: &kublingv1.Value_StringValue{StringValue: value}},
	}}}
}

func equality(field string, value string) *providerv1.Expression {
	return &providerv1.Expression{Kind: &providerv1.Expression_Comparison{Comparison: &providerv1.ComparisonExpression{
		Operator: providerv1.ComparisonOperator_COMPARISON_OPERATOR_EQUAL,
		Left:     fieldExpression(field),
		Right:    stringLiteral(value),
	}}}
}

func and(operands ...*providerv1.Expression) *providerv1.Expression {
	return &providerv1.Expression{Kind: &providerv1.Expression_Logical{Logical: &providerv1.LogicalExpression{
		Operator: providerv1.LogicalOperator_LOGICAL_OPERATOR_AND,
		Operands: operands,
	}}}
}
