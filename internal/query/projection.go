package query

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	kublingv1 "github.com/kubling-community/kubling-grpc/sdk-go/kubling/v1"
	"github.com/kubling-community/kubling-provider-redfish/internal/catalog"
	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
)

type resourceRow struct {
	uri        string
	payload    map[string]any
	observedAt time.Time
	context    map[string]string
	branchID   string
	variant    string
}

func projectRow(plan queryPlan, target device.Device, row resourceRow) (*providerv1.Tuple, error) {
	values := make([]*kublingv1.Value, 0, len(plan.projections))
	for _, projection := range plan.projections {
		value, err := projectColumn(projection.column, target, row)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", projection.column.Name, err)
		}
		values = append(values, value)
	}
	return &providerv1.Tuple{Values: values}, nil
}

func projectColumn(column catalog.Column, target device.Device, row resourceRow) (*kublingv1.Value, error) {
	mapping, mapped := columnMapping(column, row.branchID)
	if mapped {
		if mapping.Null {
			return nullValue(), nil
		}
		if mapping.Predicate != nil {
			matches, err := matchesPredicate(row.payload, mapping.Predicate)
			if err != nil {
				return nil, err
			}
			if !matches {
				return nullValue(), nil
			}
		}
	}
	switch column.Source {
	case catalog.ColumnSourceNamespace:
		return stringValue(target.Namespace()), nil
	case catalog.ColumnSourceDeviceID:
		return stringValue(target.ID()), nil
	case catalog.ColumnSourceResourceURI:
		if row.uri == "" {
			return nil, errors.New("canonical resource URI is missing")
		}
		return stringValue(row.uri), nil
	case catalog.ColumnSourceObservedAt:
		if row.observedAt.IsZero() {
			return nil, errors.New("observation time is missing")
		}
		return timestampValue(row.observedAt), nil
	case catalog.ColumnSourceContext:
		value, exists := row.context[column.ContextKey]
		if !exists || value == "" {
			return nil, errors.New("generated traversal context is missing")
		}
		return stringValue(value), nil
	case catalog.ColumnSourceVariant:
		if row.variant == "" {
			return nil, errors.New("generated traversal variant is missing")
		}
		return stringValue(row.variant), nil
	case catalog.ColumnSourcePayload:
		pointer := column.JSONPointer
		if mapped {
			pointer = mapping.JSONPointer
		}
		raw, exists, err := lookupJSONPointer(row.payload, pointer)
		if err != nil {
			return nil, err
		}
		if !exists || raw == nil {
			if !column.Nullable {
				return nil, errors.New("required Redfish property is missing or null")
			}
			return nullValue(), nil
		}
		return jsonValue(column.Type, raw)
	default:
		return nil, fmt.Errorf("unsupported column source %q", column.Source)
	}
}

func columnMapping(column catalog.Column, branchID string) (catalog.ColumnMapping, bool) {
	if branchID == "" {
		return catalog.ColumnMapping{}, false
	}
	for _, mapping := range column.Mappings {
		if mapping.BranchID == branchID {
			return mapping, true
		}
	}
	return catalog.ColumnMapping{}, false
}

func matchesPredicate(payload map[string]any, predicate *catalog.Predicate) (bool, error) {
	if predicate == nil {
		return true, nil
	}
	raw, exists, err := lookupJSONPointer(payload, predicate.JSONPointer)
	if err != nil {
		return false, err
	}
	if !exists || raw == nil {
		return false, nil
	}
	value, ok := raw.(string)
	if !ok {
		return false, expectedJSONType("string", raw)
	}
	return value == predicate.Equals, nil
}

func lookupJSONPointer(document map[string]any, pointer string) (any, bool, error) {
	if pointer == "" || !strings.HasPrefix(pointer, "/") {
		return nil, false, fmt.Errorf("invalid JSON pointer %q", pointer)
	}
	current := any(document)
	for _, rawToken := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		token, err := decodePointerToken(rawToken)
		if err != nil || token == "" {
			return nil, false, fmt.Errorf("invalid JSON pointer %q", pointer)
		}
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("JSON pointer %q traverses a non-object", pointer)
		}
		value, exists := object[token]
		if !exists {
			return nil, false, nil
		}
		current = value
	}
	return current, true, nil
}

func decodePointerToken(token string) (string, error) {
	var result strings.Builder
	for index := 0; index < len(token); index++ {
		if token[index] != '~' {
			result.WriteByte(token[index])
			continue
		}
		if index+1 >= len(token) {
			return "", errors.New("trailing JSON pointer escape")
		}
		index++
		switch token[index] {
		case '0':
			result.WriteByte('~')
		case '1':
			result.WriteByte('/')
		default:
			return "", errors.New("unknown JSON pointer escape")
		}
	}
	return result.String(), nil
}

func jsonValue(valueType kublingv1.ValueType, raw any) (*kublingv1.Value, error) {
	switch valueType {
	case kublingv1.ValueType_VALUE_TYPE_STRING:
		value, ok := raw.(string)
		if !ok {
			return nil, expectedJSONType("string", raw)
		}
		return stringValue(value), nil
	case kublingv1.ValueType_VALUE_TYPE_BOOLEAN:
		value, ok := raw.(bool)
		if !ok {
			return nil, expectedJSONType("boolean", raw)
		}
		return &kublingv1.Value{Kind: &kublingv1.Value_BooleanValue{BooleanValue: value}}, nil
	case kublingv1.ValueType_VALUE_TYPE_LONG:
		value, ok := raw.(json.Number)
		if !ok || strings.ContainsAny(value.String(), ".eE") {
			return nil, expectedJSONType("integer", raw)
		}
		parsed, err := strconv.ParseInt(value.String(), 10, 64)
		if err != nil {
			return nil, errors.New("JSON integer is outside the signed 64-bit range")
		}
		return &kublingv1.Value{Kind: &kublingv1.Value_LongValue{LongValue: parsed}}, nil
	case kublingv1.ValueType_VALUE_TYPE_DOUBLE:
		value, ok := raw.(json.Number)
		if !ok {
			return nil, expectedJSONType("number", raw)
		}
		parsed, err := value.Float64()
		if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
			return nil, errors.New("JSON number is outside the supported range")
		}
		return &kublingv1.Value{Kind: &kublingv1.Value_DoubleValue{DoubleValue: parsed}}, nil
	case kublingv1.ValueType_VALUE_TYPE_JSON:
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil, errors.New("encode JSON value")
		}
		return &kublingv1.Value{Kind: &kublingv1.Value_JsonValue{JsonValue: string(encoded)}}, nil
	default:
		return nil, fmt.Errorf("unsupported generated Redfish type %s", valueType)
	}
}

func expectedJSONType(expected string, raw any) error {
	return fmt.Errorf("expected JSON %s, got %T", expected, raw)
}

func stringValue(value string) *kublingv1.Value {
	return &kublingv1.Value{Kind: &kublingv1.Value_StringValue{StringValue: value}}
}

func timestampValue(value time.Time) *kublingv1.Value {
	return &kublingv1.Value{Kind: &kublingv1.Value_TimestampValue{
		TimestampValue: value.UTC().Format("2006-01-02T15:04:05.999999999"),
	}}
}

func nullValue() *kublingv1.Value {
	return &kublingv1.Value{Kind: &kublingv1.Value_NullValue{NullValue: &kublingv1.NullValue{}}}
}
