package query

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	kublingv1 "github.com/kubling-community/kubling-grpc/sdk-go/kubling/v1"
	"github.com/kubling-community/kubling-provider-redfish/internal/catalog"
	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	"github.com/kubling-community/kubling-provider-redfish/internal/secret"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
)

func TestProjectRowProducesTypedValuesAndNulls(t *testing.T) {
	plan, err := buildPlan(&providerv1.QueryRequest{
		Entity: &providerv1.EntityReference{Name: "SYSTEMS", Namespace: catalog.Namespace},
		Projections: []*providerv1.Projection{
			fieldProjection("namespace", ""),
			fieldProjection("device_id", ""),
			fieldProjection("resource_uri", ""),
			fieldProjection("model", ""),
			fieldProjection("status_health", ""),
			fieldProjection("observed_at", ""),
		},
	})
	if err != nil {
		t.Fatalf("buildPlan() error = %v", err)
	}
	observedAt := time.Date(2026, time.October, 22, 12, 34, 56, 789000000, time.FixedZone("test", 2*60*60))
	tuple, err := projectRow(plan, projectionTestDevice(t), resourceRow{
		uri: "/redfish/v1/Systems/1",
		payload: map[string]any{
			"@odata.id":   "/redfish/v1/Systems/1",
			"@odata.type": "#ComputerSystem.v1_29_0.ComputerSystem",
			"Id":          "system-1",
			"Name":        "Synthetic system",
			"Model":       "Model A",
		},
		observedAt: observedAt,
	})
	if err != nil {
		t.Fatalf("projectRow() error = %v", err)
	}
	values := tuple.GetValues()
	if len(values) != 6 || values[0].GetStringValue() != "dc-a" ||
		values[1].GetStringValue() != "bmc-01" ||
		values[2].GetStringValue() != "/redfish/v1/Systems/1" ||
		values[3].GetStringValue() != "Model A" ||
		values[5].GetTimestampValue() != "2026-10-22T10:34:56.789" {
		t.Fatalf("projected values = %v", values)
	}
	if _, ok := values[4].GetKind().(*kublingv1.Value_NullValue); !ok {
		t.Fatalf("missing nullable status_health = %v, want null", values[4])
	}
}

func TestProjectRowRejectsMissingRequiredAndWrongTypedProperties(t *testing.T) {
	table, ok := catalog.LookupTable("SYSTEMS")
	if !ok {
		t.Fatal("SYSTEMS catalog is missing")
	}
	base := map[string]any{
		"@odata.id":   "/redfish/v1/Systems/1",
		"@odata.type": "#ComputerSystem.v1_29_0.ComputerSystem",
		"Id":          "system-1",
		"Name":        "Synthetic system",
	}
	tests := map[string]struct {
		column  string
		payload map[string]any
	}{
		"missing required": {column: "redfish_id", payload: map[string]any{
			"@odata.id":   base["@odata.id"],
			"@odata.type": base["@odata.type"],
			"Name":        base["Name"],
		}},
		"wrong optional type": {column: "model", payload: map[string]any{
			"@odata.id":   base["@odata.id"],
			"@odata.type": base["@odata.type"],
			"Id":          base["Id"],
			"Name":        base["Name"],
			"Model":       json.Number("42"),
		}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			column := catalogColumn(t, table, test.column)
			_, err := projectColumn(column, projectionTestDevice(t), resourceRow{
				uri:        "/redfish/v1/Systems/1",
				payload:    test.payload,
				observedAt: time.Now(),
			})
			if err == nil || strings.Contains(err.Error(), "42") {
				t.Fatalf("projectColumn() error = %v", err)
			}
		})
	}
}

func TestJSONValueSupportsGeneratedScalarSurface(t *testing.T) {
	tests := []struct {
		typeName kublingv1.ValueType
		raw      any
		check    func(*kublingv1.Value) bool
	}{
		{kublingv1.ValueType_VALUE_TYPE_BOOLEAN, true, func(value *kublingv1.Value) bool { return value.GetBooleanValue() }},
		{kublingv1.ValueType_VALUE_TYPE_LONG, json.Number("42"), func(value *kublingv1.Value) bool { return value.GetLongValue() == 42 }},
		{kublingv1.ValueType_VALUE_TYPE_DOUBLE, json.Number("4.25"), func(value *kublingv1.Value) bool { return value.GetDoubleValue() == 4.25 }},
		{kublingv1.ValueType_VALUE_TYPE_JSON, map[string]any{"value": json.Number("1")}, func(value *kublingv1.Value) bool {
			return value.GetJsonValue() == `{"value":1}`
		}},
	}
	for _, test := range tests {
		value, err := jsonValue(test.typeName, test.raw)
		if err != nil || !test.check(value) {
			t.Fatalf("jsonValue(%s) = (%v, %v)", test.typeName, value, err)
		}
	}
}

func catalogColumn(t *testing.T, table catalog.Table, name string) catalog.Column {
	t.Helper()
	for _, column := range table.Columns {
		if column.Name == name {
			return column
		}
	}
	t.Fatalf("column %s.%s is missing", table.Name, name)
	return catalog.Column{}
}

func projectionTestDevice(t *testing.T) device.Device {
	t.Helper()
	username, err := secret.NewValue("operator")
	if err != nil {
		t.Fatalf("secret.NewValue(username) error = %v", err)
	}
	password, err := secret.NewValue("password")
	if err != nil {
		t.Fatalf("secret.NewValue(password) error = %v", err)
	}
	configured, err := device.New(device.Definition{
		Namespace:      "dc-a",
		ID:             "bmc-01",
		Endpoint:       "https://bmc.example.invalid",
		CredentialName: "test",
		Credential:     device.NewCredential(username, password),
		AuthMode:       device.AuthModeBasic,
		TLS:            device.NewTLSConfig("", "", false),
	})
	if err != nil {
		t.Fatalf("device.New() error = %v", err)
	}
	return configured
}
