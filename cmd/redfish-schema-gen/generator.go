package main

import (
	"errors"
	"fmt"
	"go/format"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var (
	tableNamePattern  = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	columnNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

type compiledDiscovery struct {
	kind     string
	uri      string
	pointer  string
	routes   []generatedTraversalRoute
	branches []compiledBranch
}

type compiledBranch struct {
	id       string
	variant  string
	resource resourceReference
	contexts map[string]struct{}
	mappings map[string]profileColumnMapping
}

func generate(profilePath string, lockPath string) ([]byte, error) {
	profile, err := loadProfile(profilePath)
	if err != nil {
		return nil, err
	}
	schemas, err := loadSchemaSet(lockPath)
	if err != nil {
		return nil, err
	}
	compiled, err := compileProfile(profile, schemas)
	if err != nil {
		return nil, fmt.Errorf("compile catalog profile %q: %w", profilePath, err)
	}
	contents, err := renderCatalog(compiled)
	if err != nil {
		return nil, fmt.Errorf("render catalog profile %q: %w", profilePath, err)
	}
	formatted, err := format.Source(contents)
	if err != nil {
		return nil, fmt.Errorf("format generated catalog: %w", err)
	}
	return formatted, nil
}

func compileProfile(input profile, schemas *schemaSet) (generatedCatalog, error) {
	if schemas == nil {
		return generatedCatalog{}, errors.New("schema set is required")
	}
	if input.FormatVersion != 2 {
		return generatedCatalog{}, fmt.Errorf("unsupported profile format %d", input.FormatVersion)
	}
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Namespace) == "" {
		return generatedCatalog{}, errors.New("profile name and namespace are required")
	}
	if len(input.Tables) == 0 {
		return generatedCatalog{}, errors.New("profile contains no tables")
	}
	if err := validateResourceReference(input.ServiceRoot, schemas); err != nil {
		return generatedCatalog{}, fmt.Errorf("serviceRoot: %w", err)
	}

	resolver := schemaResolver{documents: schemas.documents}
	result := generatedCatalog{
		ProfileName: strings.TrimSpace(input.Name),
		Namespace:   strings.TrimSpace(input.Namespace),
		Release:     schemas.lock.Release,
		Commit:      schemas.lock.Commit,
		Tables:      make([]generatedTable, 0, len(input.Tables)),
	}
	seenTables := make(map[string]struct{}, len(input.Tables))
	for index, table := range input.Tables {
		compiled, err := compileTable(table, input.ServiceRoot, resolver, schemas)
		if err != nil {
			return generatedCatalog{}, fmt.Errorf("tables[%d] %q: %w", index, table.Name, err)
		}
		key := strings.ToUpper(compiled.Name)
		if _, exists := seenTables[key]; exists {
			return generatedCatalog{}, fmt.Errorf("duplicate table %q", compiled.Name)
		}
		seenTables[key] = struct{}{}
		result.Tables = append(result.Tables, compiled)
	}
	return result, nil
}

func compileTable(
	input profileTable,
	serviceRoot resourceReference,
	resolver schemaResolver,
	schemas *schemaSet,
) (generatedTable, error) {
	name := strings.TrimSpace(input.Name)
	if !tableNamePattern.MatchString(name) {
		return generatedTable{}, fmt.Errorf("invalid logical table name %q", input.Name)
	}
	annotation := normalizeText(input.Annotation)
	if annotation == "" {
		return generatedTable{}, errors.New("table annotation is required")
	}
	discovery, err := compileDiscovery(
		input.Discovery,
		serviceRoot,
		resolver,
		schemas,
	)
	if err != nil {
		return generatedTable{}, fmt.Errorf("discovery: %w", err)
	}
	registryBacked := discovery.kind == "DiscoveryRegistry"
	if registryBacked {
		if input.Resource.Schema != "" || input.Resource.Definition != "" {
			return generatedTable{}, errors.New("registry discovery forbids a Redfish resource")
		}
	} else if err := validateResourceReference(input.Resource, schemas); err != nil {
		return generatedTable{}, fmt.Errorf("resource: %w", err)
	}
	if len(input.Columns) == 0 {
		return generatedTable{}, errors.New("at least one column is required")
	}

	result := generatedTable{
		Name:             name,
		Annotation:       annotation,
		SchemaFile:       input.Resource.Schema,
		SchemaDefinition: input.Resource.Definition,
		DiscoveryKind:    discovery.kind,
		DiscoveryURI:     discovery.uri,
		DiscoveryPointer: discovery.pointer,
		TraversalRoutes:  discovery.routes,
		Columns:          make([]generatedColumn, 0, len(input.Columns)),
		StableKey:        append([]string(nil), input.StableKey...),
		Routing:          append([]string(nil), input.Routing...),
	}
	seenColumns := make(map[string]struct{}, len(input.Columns))
	for index, column := range input.Columns {
		var compiled generatedColumn
		var err error
		if registryBacked {
			compiled, err = compileRegistryColumn(column)
		} else if discovery.kind == "DiscoveryGraph" {
			compiled, err = compileGraphColumn(column, discovery.branches, resolver)
		} else {
			compiled, err = compileColumn(column, input.Resource, resolver)
		}
		if err != nil {
			return generatedTable{}, fmt.Errorf("columns[%d] %q: %w", index, column.Name, err)
		}
		key := strings.ToUpper(compiled.Name)
		if _, exists := seenColumns[key]; exists {
			return generatedTable{}, fmt.Errorf("duplicate column %q", compiled.Name)
		}
		seenColumns[key] = struct{}{}
		result.Columns = append(result.Columns, compiled)
	}
	if registryBacked {
		if !slices.Equal(result.StableKey, []string{"namespace"}) {
			return generatedTable{}, fmt.Errorf("registry stableKey must be [namespace], found %v", result.StableKey)
		}
		if len(result.Routing) != 0 {
			return generatedTable{}, fmt.Errorf("registry routing must be empty, found %v", result.Routing)
		}
	} else {
		if len(result.StableKey) < 3 || !slices.Equal(result.StableKey[:2], []string{"namespace", "device_id"}) {
			return generatedTable{}, fmt.Errorf("stableKey must begin with [namespace device_id] and contain an identity component, found %v", result.StableKey)
		}
		if !slices.Equal(result.Routing, []string{"namespace", "device_id"}) {
			return generatedTable{}, fmt.Errorf("routing must be [namespace device_id], found %v", result.Routing)
		}
	}
	columns := make(map[string]generatedColumn, len(result.Columns))
	for _, column := range result.Columns {
		columns[column.Name] = column
	}
	for _, component := range result.StableKey {
		column, exists := columns[component]
		if !exists {
			return generatedTable{}, fmt.Errorf("stable-key column %q does not exist", component)
		}
		if column.Nullable {
			return generatedTable{}, fmt.Errorf("stable-key column %q is nullable", component)
		}
	}
	for _, route := range result.Routing {
		column, exists := columns[route]
		if !exists {
			return generatedTable{}, fmt.Errorf("routing column %q does not exist", route)
		}
		if !column.Searchable {
			return generatedTable{}, fmt.Errorf("routing column %q is not equality searchable", route)
		}
	}
	if discovery.kind == "DiscoveryGraph" {
		for _, branch := range discovery.branches {
			for name := range branch.mappings {
				column, exists := columns[name]
				if !exists {
					return generatedTable{}, fmt.Errorf("branch %q maps unknown column %q", branch.id, name)
				}
				if column.SourceConstant != "ColumnSourcePayload" {
					return generatedTable{}, fmt.Errorf("branch %q maps non-payload column %q", branch.id, name)
				}
			}
		}
	}
	return result, nil
}

func compileDiscovery(
	input profileDiscovery,
	serviceRoot resourceReference,
	resolver schemaResolver,
	schemas *schemaSet,
) (compiledDiscovery, error) {
	switch input.Kind {
	case "registry":
		if input.URI != "" || input.Pointer != "" || len(input.Routes) != 0 {
			return compiledDiscovery{}, errors.New("registry discovery forbids uri, pointer and routes")
		}
		return compiledDiscovery{kind: "DiscoveryRegistry"}, nil
	case "singleton":
		if input.Pointer != "" || input.URI == "" || len(input.Routes) != 0 {
			return compiledDiscovery{}, errors.New("singleton discovery requires uri and forbids pointer and routes")
		}
		parsed, err := url.ParseRequestURI(input.URI)
		if err != nil || parsed.IsAbs() || !strings.HasPrefix(parsed.Path, "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return compiledDiscovery{}, fmt.Errorf("singleton URI %q must be an absolute path without query or fragment", input.URI)
		}
		return compiledDiscovery{kind: "DiscoverySingleton", uri: input.URI}, nil
	case "collection":
		if input.URI != "" || input.Pointer == "" || len(input.Routes) != 0 {
			return compiledDiscovery{}, errors.New("collection discovery requires pointer and forbids uri and routes")
		}
		resolved, err := resolver.resolveProperty(serviceRoot.Schema, serviceRoot.Definition, input.Pointer)
		if err != nil {
			return compiledDiscovery{}, err
		}
		if resolved.logicalType != "VALUE_TYPE_STRING" {
			return compiledDiscovery{}, fmt.Errorf("collection pointer %q resolves to %s, want string", input.Pointer, resolved.nativeType)
		}
		return compiledDiscovery{kind: "DiscoveryCollection", pointer: input.Pointer}, nil
	case "graph":
		if input.URI != "" || input.Pointer != "" || len(input.Routes) == 0 {
			return compiledDiscovery{}, errors.New("graph discovery requires routes and forbids uri and pointer")
		}
		return compileTraversalRoutes(input.Routes, serviceRoot, resolver, schemas)
	default:
		return compiledDiscovery{}, fmt.Errorf("unsupported discovery kind %q", input.Kind)
	}
}

func compileRegistryColumn(input profileColumn) (generatedColumn, error) {
	name := strings.TrimSpace(input.Name)
	if !columnNamePattern.MatchString(name) || name == "identifier" {
		return generatedColumn{}, fmt.Errorf("invalid provider column name %q", input.Name)
	}
	if input.Pointer != "" || input.Context != "" {
		return generatedColumn{}, errors.New("registry column forbids pointer and context")
	}
	result := generatedColumn{
		Name:       name,
		Annotation: normalizeText(input.Annotation),
	}
	switch input.Source {
	case "namespace":
		result.SourceConstant = "ColumnSourceNamespace"
		result.TypeConstant = "VALUE_TYPE_STRING"
		result.NativeType = "provider:string"
		result.Searchable = true
	case "device_count":
		result.SourceConstant = "ColumnSourceDeviceCount"
		result.TypeConstant = "VALUE_TYPE_LONG"
		result.NativeType = "provider:int64"
	default:
		return generatedColumn{}, fmt.Errorf("unsupported registry column source %q", input.Source)
	}
	if result.Annotation == "" {
		return generatedColumn{}, errors.New("registry column annotation is required")
	}
	return result, nil
}

func compileTraversalRoutes(
	input []profileTraversalRoute,
	serviceRoot resourceReference,
	resolver schemaResolver,
	schemas *schemaSet,
) (compiledDiscovery, error) {
	result := compiledDiscovery{kind: "DiscoveryGraph"}
	seenBranchNames := make(map[string]struct{})
	for routeIndex, route := range input {
		mode := ""
		switch route.Mode {
		case "union":
			mode = "TraversalUnion"
		case "fallback":
			mode = "TraversalFallback"
		default:
			return compiledDiscovery{}, fmt.Errorf("routes[%d]: mode must be union or fallback", routeIndex)
		}
		if len(route.Branches) == 0 {
			return compiledDiscovery{}, fmt.Errorf("routes[%d]: at least one branch is required", routeIndex)
		}
		prefix, prefixResource, prefixContexts, err := compileTraversalSteps(
			route.Prefix,
			serviceRoot,
			nil,
			resolver,
			schemas,
		)
		if err != nil {
			return compiledDiscovery{}, fmt.Errorf("routes[%d].prefix: %w", routeIndex, err)
		}
		compiledRoute := generatedTraversalRoute{
			Prefix:   prefix,
			Mode:     mode,
			Branches: make([]generatedTraversalBranch, 0, len(route.Branches)),
		}
		for branchIndex, branch := range route.Branches {
			name := strings.TrimSpace(branch.Name)
			if !columnNamePattern.MatchString(name) {
				return compiledDiscovery{}, fmt.Errorf("routes[%d].branches[%d]: invalid name %q", routeIndex, branchIndex, branch.Name)
			}
			if _, exists := seenBranchNames[name]; exists {
				return compiledDiscovery{}, fmt.Errorf("duplicate traversal branch name %q", name)
			}
			seenBranchNames[name] = struct{}{}
			if len(branch.Steps) == 0 {
				return compiledDiscovery{}, fmt.Errorf("routes[%d].branches[%d]: at least one step is required", routeIndex, branchIndex)
			}
			steps, terminal, contexts, err := compileTraversalSteps(
				branch.Steps,
				prefixResource,
				prefixContexts,
				resolver,
				schemas,
			)
			if err != nil {
				return compiledDiscovery{}, fmt.Errorf("routes[%d].branches[%d] %q: %w", routeIndex, branchIndex, name, err)
			}
			compiledRoute.Branches = append(compiledRoute.Branches, generatedTraversalBranch{
				ID:      name,
				Variant: strings.TrimSpace(branch.Variant),
				Steps:   steps,
			})
			result.branches = append(result.branches, compiledBranch{
				id:       name,
				variant:  strings.TrimSpace(branch.Variant),
				resource: terminal,
				contexts: contexts,
				mappings: branch.Mappings,
			})
		}
		result.routes = append(result.routes, compiledRoute)
	}
	return result, nil
}

func compileTraversalSteps(
	input []profileTraversalStep,
	start resourceReference,
	inherited map[string]struct{},
	resolver schemaResolver,
	schemas *schemaSet,
) ([]generatedTraversalStep, resourceReference, map[string]struct{}, error) {
	current := start
	contexts := cloneStringSet(inherited)
	result := make([]generatedTraversalStep, 0, len(input))
	for index, step := range input {
		if err := validateResourceReference(step.Resource, schemas); err != nil {
			return nil, resourceReference{}, nil, fmt.Errorf("steps[%d].resource: %w", index, err)
		}
		resolved, err := resolver.resolveProperty(current.Schema, current.Definition, step.Pointer)
		if err != nil {
			return nil, resourceReference{}, nil, fmt.Errorf("steps[%d].pointer: %w", index, err)
		}
		kind := ""
		expectedType := ""
		switch step.Kind {
		case "singleton":
			kind, expectedType = "TraversalSingleton", "VALUE_TYPE_STRING"
		case "collection":
			kind, expectedType = "TraversalCollection", "VALUE_TYPE_STRING"
		case "collection_links":
			kind, expectedType = "TraversalCollectionLinks", "VALUE_TYPE_STRING"
		case "linked":
			kind, expectedType = "TraversalLinked", "VALUE_TYPE_JSON"
		case "links":
			kind, expectedType = "TraversalLinks", "VALUE_TYPE_JSON"
		case "embedded":
			kind, expectedType = "TraversalEmbedded", "VALUE_TYPE_JSON"
		default:
			return nil, resourceReference{}, nil, fmt.Errorf("steps[%d]: unsupported kind %q", index, step.Kind)
		}
		if resolved.logicalType != expectedType ||
			(expectedType == "VALUE_TYPE_JSON" && resolved.nativeType != "array") {
			return nil, resourceReference{}, nil, fmt.Errorf(
				"steps[%d].pointer %q resolves to %s, incompatible with %s",
				index,
				step.Pointer,
				resolved.nativeType,
				step.Kind,
			)
		}
		capture := strings.TrimSpace(step.Capture)
		if capture != "" {
			if !columnNamePattern.MatchString(capture) {
				return nil, resourceReference{}, nil, fmt.Errorf("steps[%d]: invalid capture %q", index, capture)
			}
			if _, exists := contexts[capture]; exists {
				return nil, resourceReference{}, nil, fmt.Errorf("steps[%d]: duplicate capture %q", index, capture)
			}
			contexts[capture] = struct{}{}
		}
		predicatePointer, predicateEquals, err := compilePredicate(step.Where, step.Resource, resolver)
		if err != nil {
			return nil, resourceReference{}, nil, fmt.Errorf("steps[%d].where: %w", index, err)
		}
		result = append(result, generatedTraversalStep{
			Kind:             kind,
			Pointer:          step.Pointer,
			SchemaFile:       step.Resource.Schema,
			SchemaDefinition: step.Resource.Definition,
			Capture:          capture,
			PredicatePointer: predicatePointer,
			PredicateEquals:  predicateEquals,
		})
		current = step.Resource
	}
	return result, current, contexts, nil
}

func compileGraphColumn(
	input profileColumn,
	branches []compiledBranch,
	resolver schemaResolver,
) (generatedColumn, error) {
	name := strings.TrimSpace(input.Name)
	if !columnNamePattern.MatchString(name) || name == "identifier" {
		return generatedColumn{}, fmt.Errorf("invalid provider column name %q", input.Name)
	}
	if len(branches) == 0 {
		return generatedColumn{}, errors.New("graph column requires traversal branches")
	}
	result := generatedColumn{Name: name, Pointer: input.Pointer, Context: strings.TrimSpace(input.Context), Annotation: normalizeText(input.Annotation)}
	switch input.Source {
	case "namespace":
		if input.Pointer != "" || input.Context != "" {
			return generatedColumn{}, errors.New("namespace source forbids pointer and context")
		}
		result.SourceConstant = "ColumnSourceNamespace"
		result.TypeConstant = "VALUE_TYPE_STRING"
		result.NativeType = "provider:string"
		result.Searchable = true
	case "device_id":
		if input.Pointer != "" || input.Context != "" {
			return generatedColumn{}, errors.New("device_id source forbids pointer and context")
		}
		result.SourceConstant = "ColumnSourceDeviceID"
		result.TypeConstant = "VALUE_TYPE_STRING"
		result.NativeType = "provider:string"
		result.Searchable = true
	case "observed_at":
		if input.Pointer != "" || input.Context != "" {
			return generatedColumn{}, errors.New("observed_at source forbids pointer and context")
		}
		result.SourceConstant = "ColumnSourceObservedAt"
		result.TypeConstant = "VALUE_TYPE_TIMESTAMP"
		result.NativeType = "provider:timestamp"
	case "context":
		if input.Pointer != "" || result.Context == "" || !columnNamePattern.MatchString(result.Context) {
			return generatedColumn{}, errors.New("context source requires a valid context and forbids pointer")
		}
		for _, branch := range branches {
			if _, exists := branch.contexts[result.Context]; !exists {
				return generatedColumn{}, fmt.Errorf("context %q is not captured by branch %q", result.Context, branch.id)
			}
		}
		result.SourceConstant = "ColumnSourceContext"
		result.TypeConstant = "VALUE_TYPE_STRING"
		result.NativeType = "provider:string"
	case "variant":
		if input.Pointer != "" || input.Context != "" {
			return generatedColumn{}, errors.New("variant source forbids pointer and context")
		}
		for _, branch := range branches {
			if branch.variant == "" {
				return generatedColumn{}, fmt.Errorf("branch %q has no variant value", branch.id)
			}
		}
		result.SourceConstant = "ColumnSourceVariant"
		result.TypeConstant = "VALUE_TYPE_STRING"
		result.NativeType = "provider:string"
	case "resource_uri":
		if input.Pointer == "" || input.Context != "" {
			return generatedColumn{}, errors.New("resource_uri source requires pointer and forbids context")
		}
		for _, branch := range branches {
			resolved, err := resolver.resolveProperty(branch.resource.Schema, branch.resource.Definition, input.Pointer)
			if err != nil {
				return generatedColumn{}, fmt.Errorf("branch %q: %w", branch.id, err)
			}
			if resolved.logicalType != "VALUE_TYPE_STRING" {
				return generatedColumn{}, fmt.Errorf("branch %q resource_uri must resolve to a string", branch.id)
			}
		}
		result.SourceConstant = "ColumnSourceResourceURI"
		result.TypeConstant = "VALUE_TYPE_STRING"
		result.NativeType = "json-schema:string"
		result.Searchable = true
	case "payload":
		if input.Context != "" {
			return generatedColumn{}, errors.New("payload source forbids context")
		}
		if err := compileGraphPayloadColumn(&result, input, branches, resolver); err != nil {
			return generatedColumn{}, err
		}
	default:
		return generatedColumn{}, fmt.Errorf("unsupported column source %q", input.Source)
	}
	if result.Annotation == "" {
		return generatedColumn{}, errors.New("column annotation could not be derived and must be provided")
	}
	return result, nil
}

func compileGraphPayloadColumn(
	result *generatedColumn,
	input profileColumn,
	branches []compiledBranch,
	resolver schemaResolver,
) error {
	commonPointer := ""
	common := true
	for _, branch := range branches {
		mapping, exists := branch.mappings[result.Name]
		if !exists {
			mapping.Pointer = input.Pointer
		}
		if mapping.Null {
			if mapping.Pointer != "" || mapping.When != nil {
				return fmt.Errorf("branch %q null mapping forbids pointer and predicate", branch.id)
			}
			result.Nullable = true
			result.Mappings = append(result.Mappings, generatedColumnMapping{BranchID: branch.id, Null: true})
			common = false
			continue
		}
		if mapping.Pointer == "" {
			return fmt.Errorf("branch %q requires a payload pointer or null mapping", branch.id)
		}
		resolved, err := resolver.resolveProperty(branch.resource.Schema, branch.resource.Definition, mapping.Pointer)
		if err != nil {
			return fmt.Errorf("branch %q: %w", branch.id, err)
		}
		if err := mergeColumnType(result, resolved); err != nil {
			return fmt.Errorf("branch %q: %w", branch.id, err)
		}
		predicatePointer, predicateEquals, err := compilePredicate(mapping.When, branch.resource, resolver)
		if err != nil {
			return fmt.Errorf("branch %q predicate: %w", branch.id, err)
		}
		if mapping.When != nil {
			result.Nullable = true
		}
		result.Nullable = result.Nullable || resolved.nullable
		if result.Annotation == "" {
			result.Annotation = resolved.description
		}
		result.Mappings = append(result.Mappings, generatedColumnMapping{
			BranchID:         branch.id,
			Pointer:          mapping.Pointer,
			PredicatePointer: predicatePointer,
			PredicateEquals:  predicateEquals,
		})
		if commonPointer == "" {
			commonPointer = mapping.Pointer
		} else if commonPointer != mapping.Pointer {
			common = false
		}
	}
	if result.TypeConstant == "" {
		return errors.New("payload column is null in every traversal branch")
	}
	result.SourceConstant = "ColumnSourcePayload"
	if common {
		result.Pointer = commonPointer
	} else {
		result.Pointer = ""
	}
	return nil
}

func mergeColumnType(result *generatedColumn, resolved propertyResolution) error {
	logicalType := resolved.logicalType
	nativeType := resolved.nativeType
	if result.TypeConstant == "" {
		result.TypeConstant = logicalType
		result.NativeType = "json-schema:" + nativeType
		return nil
	}
	if result.TypeConstant == logicalType {
		return nil
	}
	if (result.TypeConstant == "VALUE_TYPE_LONG" && logicalType == "VALUE_TYPE_DOUBLE") ||
		(result.TypeConstant == "VALUE_TYPE_DOUBLE" && logicalType == "VALUE_TYPE_LONG") {
		result.TypeConstant = "VALUE_TYPE_DOUBLE"
		result.NativeType = "json-schema:number"
		return nil
	}
	return fmt.Errorf("payload mappings have incompatible logical types %s and %s", result.TypeConstant, logicalType)
}

func compilePredicate(
	input *profilePredicate,
	resource resourceReference,
	resolver schemaResolver,
) (string, string, error) {
	if input == nil {
		return "", "", nil
	}
	if input.Pointer == "" || input.Equals == "" {
		return "", "", errors.New("predicate requires pointer and equals")
	}
	resolved, err := resolver.resolveProperty(resource.Schema, resource.Definition, input.Pointer)
	if err != nil {
		return "", "", err
	}
	if resolved.logicalType != "VALUE_TYPE_STRING" {
		return "", "", fmt.Errorf("predicate pointer %q resolves to %s, want string", input.Pointer, resolved.nativeType)
	}
	return input.Pointer, input.Equals, nil
}

func cloneStringSet(input map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{}, len(input))
	for value := range input {
		result[value] = struct{}{}
	}
	return result
}

func compileColumn(
	input profileColumn,
	resource resourceReference,
	resolver schemaResolver,
) (generatedColumn, error) {
	name := strings.TrimSpace(input.Name)
	if !columnNamePattern.MatchString(name) || name == "identifier" {
		return generatedColumn{}, fmt.Errorf("invalid provider column name %q", input.Name)
	}
	annotation := normalizeText(input.Annotation)
	result := generatedColumn{Name: name, Pointer: input.Pointer, Annotation: annotation}
	if input.Context != "" {
		return generatedColumn{}, errors.New("non-graph column forbids context")
	}
	switch input.Source {
	case "namespace":
		if input.Pointer != "" {
			return generatedColumn{}, errors.New("namespace source forbids pointer")
		}
		result.SourceConstant = "ColumnSourceNamespace"
		result.TypeConstant = "VALUE_TYPE_STRING"
		result.NativeType = "provider:string"
		result.Searchable = true
	case "device_id":
		if input.Pointer != "" {
			return generatedColumn{}, errors.New("device_id source forbids pointer")
		}
		result.SourceConstant = "ColumnSourceDeviceID"
		result.TypeConstant = "VALUE_TYPE_STRING"
		result.NativeType = "provider:string"
		result.Searchable = true
	case "observed_at":
		if input.Pointer != "" {
			return generatedColumn{}, errors.New("observed_at source forbids pointer")
		}
		result.SourceConstant = "ColumnSourceObservedAt"
		result.TypeConstant = "VALUE_TYPE_TIMESTAMP"
		result.NativeType = "provider:timestamp"
	case "resource_uri", "payload":
		if input.Pointer == "" {
			return generatedColumn{}, fmt.Errorf("%s source requires pointer", input.Source)
		}
		resolved, err := resolver.resolveProperty(resource.Schema, resource.Definition, input.Pointer)
		if err != nil {
			return generatedColumn{}, err
		}
		result.TypeConstant = resolved.logicalType
		result.NativeType = "json-schema:" + resolved.nativeType
		result.Nullable = resolved.nullable
		if result.Annotation == "" {
			result.Annotation = resolved.description
		}
		if input.Source == "resource_uri" {
			if result.TypeConstant != "VALUE_TYPE_STRING" || result.Nullable {
				return generatedColumn{}, errors.New("resource_uri must resolve to a required string")
			}
			result.SourceConstant = "ColumnSourceResourceURI"
			result.Searchable = true
		} else {
			result.SourceConstant = "ColumnSourcePayload"
		}
	default:
		return generatedColumn{}, fmt.Errorf("unsupported column source %q", input.Source)
	}
	if result.Annotation == "" {
		return generatedColumn{}, errors.New("column annotation could not be derived and must be provided")
	}
	return result, nil
}

func validateResourceReference(reference resourceReference, schemas *schemaSet) error {
	if filepath.Base(reference.Schema) != reference.Schema || reference.Schema == "" {
		return fmt.Errorf("schema %q must be a locked JSON filename", reference.Schema)
	}
	if strings.TrimSpace(reference.Definition) == "" {
		return errors.New("definition is required")
	}
	document, exists := schemas.documents[reference.Schema]
	if !exists {
		return fmt.Errorf("schema %q is not in the upstream lock", reference.Schema)
	}
	definitions, ok := document.root["definitions"].(map[string]any)
	if !ok {
		return fmt.Errorf("schema %q has no definitions object", reference.Schema)
	}
	if _, ok := definitions[reference.Definition].(map[string]any); !ok {
		return fmt.Errorf("schema %q has no definition %q", reference.Schema, reference.Definition)
	}
	return nil
}
