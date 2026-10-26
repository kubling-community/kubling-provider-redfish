package main

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
)

type schemaResolver struct {
	documents map[string]schemaDocument
}

type unlockedSchemaReferenceError struct {
	reference string
	file      string
}

func (e *unlockedSchemaReferenceError) Error() string {
	return fmt.Sprintf("JSON Schema reference %q requires unlocked file %q", e.reference, e.file)
}

func (r schemaResolver) resolveProperty(
	file string,
	definition string,
	pointer string,
) (propertyResolution, error) {
	document, exists := r.documents[file]
	if !exists {
		return propertyResolution{}, fmt.Errorf("schema %q is not in the upstream lock", file)
	}
	definitions, ok := document.root["definitions"].(map[string]any)
	if !ok {
		return propertyResolution{}, fmt.Errorf("schema %q has no definitions object", file)
	}
	definitionNode, ok := definitions[definition].(map[string]any)
	if !ok {
		return propertyResolution{}, fmt.Errorf("schema %q has no definition %q", file, definition)
	}
	tokens, err := parseJSONPointer(pointer)
	if err != nil {
		return propertyResolution{}, err
	}
	if len(tokens) == 0 {
		return propertyResolution{}, errors.New("property JSON pointer must not be empty")
	}

	current := nodeLocation{file: file, node: definitionNode}
	nullable := false
	description := ""
	for _, token := range tokens {
		property, required, alternativeNullable, err := r.property(current, token)
		if err != nil {
			return propertyResolution{}, fmt.Errorf("resolve %s in %s#/definitions/%s: %w", pointer, file, definition, err)
		}
		nullable = nullable || alternativeNullable || !required
		if value, ok := property.node["description"].(string); ok {
			description = normalizeText(value)
		}
		current = property
	}

	alternatives, nodeNullable, err := r.alternatives(current, make(map[string]struct{}))
	if err != nil {
		return propertyResolution{}, fmt.Errorf("resolve %s type: %w", pointer, err)
	}
	nullable = nullable || nodeNullable
	logicalType := ""
	nativeType := ""
	for _, alternative := range alternatives {
		typeName, err := schemaType(alternative.node)
		if err != nil {
			return propertyResolution{}, fmt.Errorf("resolve %s type: %w", pointer, err)
		}
		mapped, err := mapLogicalType(typeName)
		if err != nil {
			return propertyResolution{}, fmt.Errorf("resolve %s type: %w", pointer, err)
		}
		if logicalType != "" && logicalType != mapped {
			return propertyResolution{}, fmt.Errorf("property %s has incompatible alternatives %s and %s", pointer, logicalType, mapped)
		}
		logicalType = mapped
		if nativeType != "" && nativeType != typeName {
			return propertyResolution{}, fmt.Errorf("property %s has incompatible JSON types %s and %s", pointer, nativeType, typeName)
		}
		nativeType = typeName
		if description == "" {
			if value, ok := alternative.node["description"].(string); ok {
				description = normalizeText(value)
			}
		}
	}
	if logicalType == "" {
		return propertyResolution{}, fmt.Errorf("property %s resolves only to null", pointer)
	}
	return propertyResolution{
		logicalType: logicalType,
		nativeType:  nativeType,
		nullable:    nullable,
		description: description,
	}, nil
}

func (r schemaResolver) property(
	location nodeLocation,
	name string,
) (nodeLocation, bool, bool, error) {
	alternatives, nullable, err := r.alternatives(location, make(map[string]struct{}))
	if err != nil {
		return nodeLocation{}, false, false, err
	}
	for _, alternative := range alternatives {
		properties, ok := alternative.node["properties"].(map[string]any)
		if !ok {
			continue
		}
		value, exists := properties[name]
		if !exists {
			continue
		}
		property, ok := value.(map[string]any)
		if !ok {
			return nodeLocation{}, false, false, fmt.Errorf("property %q is not a schema object", name)
		}
		return nodeLocation{file: alternative.file, node: property}, requiredProperty(alternative.node, name), nullable, nil
	}
	return nodeLocation{}, false, false, fmt.Errorf("property %q does not exist", name)
}

func (r schemaResolver) alternatives(
	location nodeLocation,
	stack map[string]struct{},
) ([]nodeLocation, bool, error) {
	if reference, ok := location.node["$ref"].(string); ok {
		key := location.file + "\x00" + reference
		if _, exists := stack[key]; exists {
			return nil, false, fmt.Errorf("cyclic JSON Schema reference %q", reference)
		}
		stack[key] = struct{}{}
		resolved, err := r.resolveReference(location.file, reference)
		if err != nil {
			return nil, false, err
		}
		alternatives, nullable, err := r.alternatives(resolved, stack)
		delete(stack, key)
		return alternatives, nullable, err
	}

	for _, keyword := range []string{"anyOf", "oneOf"} {
		serialized, exists := location.node[keyword]
		if !exists {
			continue
		}
		items, ok := serialized.([]any)
		if !ok || len(items) == 0 {
			return nil, false, fmt.Errorf("%s must be a non-empty array", keyword)
		}
		var result []nodeLocation
		nullable := false
		skippedUnlocked := false
		for index, item := range items {
			node, ok := item.(map[string]any)
			if !ok {
				return nil, false, fmt.Errorf("%s alternative %d is not an object", keyword, index)
			}
			alternatives, childNullable, err := r.alternatives(nodeLocation{file: location.file, node: node}, stack)
			if err != nil {
				var unlocked *unlockedSchemaReferenceError
				if errors.As(err, &unlocked) {
					skippedUnlocked = true
					continue
				}
				return nil, false, err
			}
			result = append(result, alternatives...)
			nullable = nullable || childNullable
		}
		if len(result) == 0 && skippedUnlocked {
			return nil, false, errors.New("all JSON Schema alternatives require unlocked files")
		}
		return result, nullable, nil
	}

	types, nullable, err := nodeTypes(location.node)
	if err != nil {
		return nil, false, err
	}
	if len(types) == 0 && nullable {
		return nil, true, nil
	}
	return []nodeLocation{location}, nullable, nil
}

func (r schemaResolver) resolveReference(currentFile string, reference string) (nodeLocation, error) {
	targetFile := currentFile
	fragment := ""
	if strings.HasPrefix(reference, "#") {
		fragment = strings.TrimPrefix(reference, "#")
	} else {
		parsed, err := url.Parse(reference)
		if err != nil {
			return nodeLocation{}, fmt.Errorf("parse JSON Schema reference %q: %w", reference, err)
		}
		if (parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.Host != "redfish.dmtf.org" || parsed.User != nil || parsed.RawQuery != "" ||
			!strings.HasPrefix(parsed.Path, "/schemas/v1/") {
			return nodeLocation{}, fmt.Errorf("unsupported JSON Schema reference %q", reference)
		}
		targetFile = path.Base(parsed.Path)
		fragment = parsed.Fragment
	}
	document, exists := r.documents[targetFile]
	if !exists {
		return nodeLocation{}, &unlockedSchemaReferenceError{reference: reference, file: targetFile}
	}
	value, err := lookupJSONPointer(document.root, fragment)
	if err != nil {
		return nodeLocation{}, fmt.Errorf("resolve JSON Schema reference %q: %w", reference, err)
	}
	node, ok := value.(map[string]any)
	if !ok {
		return nodeLocation{}, fmt.Errorf("JSON Schema reference %q does not select an object", reference)
	}
	return nodeLocation{file: targetFile, node: node}, nil
}

func parseJSONPointer(pointer string) ([]string, error) {
	if pointer == "" {
		return nil, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("invalid JSON pointer %q", pointer)
	}
	raw := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	result := make([]string, len(raw))
	for index, token := range raw {
		decoded, err := decodePointerToken(token)
		if err != nil {
			return nil, fmt.Errorf("invalid JSON pointer %q: %w", pointer, err)
		}
		if decoded == "" {
			return nil, fmt.Errorf("invalid JSON pointer %q: empty token", pointer)
		}
		result[index] = decoded
	}
	return result, nil
}

func decodePointerToken(token string) (string, error) {
	var result strings.Builder
	for index := 0; index < len(token); index++ {
		if token[index] != '~' {
			result.WriteByte(token[index])
			continue
		}
		if index+1 >= len(token) {
			return "", errors.New("trailing ~ escape")
		}
		index++
		switch token[index] {
		case '0':
			result.WriteByte('~')
		case '1':
			result.WriteByte('/')
		default:
			return "", fmt.Errorf("unknown ~%c escape", token[index])
		}
	}
	return result.String(), nil
}

func lookupJSONPointer(document any, pointer string) (any, error) {
	tokens, err := parseJSONPointer(pointer)
	if err != nil {
		return nil, err
	}
	current := document
	for _, token := range tokens {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("JSON pointer segment %q traverses a non-object", token)
		}
		value, exists := object[token]
		if !exists {
			return nil, fmt.Errorf("JSON pointer segment %q does not exist", token)
		}
		current = value
	}
	return current, nil
}

func requiredProperty(node map[string]any, name string) bool {
	values, ok := node["required"].([]any)
	if !ok {
		return false
	}
	for _, value := range values {
		if value == name {
			return true
		}
	}
	return false
}

func nodeTypes(node map[string]any) ([]string, bool, error) {
	serialized, exists := node["type"]
	if !exists {
		if _, ok := node["properties"].(map[string]any); ok {
			return []string{"object"}, false, nil
		}
		if _, ok := node["items"]; ok {
			return []string{"array"}, false, nil
		}
		return nil, false, nil
	}
	switch value := serialized.(type) {
	case string:
		if value == "null" {
			return nil, true, nil
		}
		return []string{value}, false, nil
	case []any:
		var result []string
		nullable := false
		for _, item := range value {
			typeName, ok := item.(string)
			if !ok {
				return nil, false, errors.New("JSON Schema type array contains a non-string")
			}
			if typeName == "null" {
				nullable = true
				continue
			}
			result = append(result, typeName)
		}
		return result, nullable, nil
	default:
		return nil, false, errors.New("JSON Schema type must be a string or string array")
	}
}

func schemaType(node map[string]any) (string, error) {
	types, _, err := nodeTypes(node)
	if err != nil {
		return "", err
	}
	if len(types) != 1 {
		return "", fmt.Errorf("expected one concrete JSON type, found %v", types)
	}
	return types[0], nil
}

func mapLogicalType(native string) (string, error) {
	switch native {
	case "string":
		return "VALUE_TYPE_STRING", nil
	case "boolean":
		return "VALUE_TYPE_BOOLEAN", nil
	case "integer":
		return "VALUE_TYPE_LONG", nil
	case "number":
		return "VALUE_TYPE_DOUBLE", nil
	case "object", "array":
		return "VALUE_TYPE_JSON", nil
	default:
		return "", fmt.Errorf("unsupported JSON Schema type %q", native)
	}
}

func normalizeText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
