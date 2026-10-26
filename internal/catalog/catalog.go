package catalog

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	kublingv1 "github.com/kubling-community/kubling-grpc/sdk-go/kubling/v1"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	providersdk "github.com/kubling-community/kubling-providers/sdk-go/provider"
)

// Tables returns a detached copy of the generated table descriptors.
func Tables() []Table {
	result := make([]Table, len(generatedTables))
	for index, table := range generatedTables {
		result[index] = cloneTable(table)
	}
	return result
}

// LookupTable returns a detached descriptor using case-insensitive logical
// table-name matching.
func LookupTable(name string) (Table, bool) {
	for _, table := range generatedTables {
		if strings.EqualFold(table.Name, strings.TrimSpace(name)) {
			return cloneTable(table), true
		}
	}
	return Table{}, false
}

// Metadata returns fresh provider-neutral metadata for the generated catalog.
func Metadata(options MetadataOptions) *providerv1.SchemaMetadata {
	metadata := &providerv1.SchemaMetadata{
		Properties: map[string]string{
			"redfish.profile":      ProfileName,
			"redfish.dmtf.release": DMTFRelease,
			"redfish.dmtf.commit":  DMTFCommit,
			"redfish.table_count":  strconv.Itoa(len(generatedTables)),
		},
		Namespaces: []*providerv1.NamespaceMetadata{{
			Name:       Namespace,
			Annotation: "Redfish infrastructure inventory and current observations",
			Properties: map[string]string{
				"redfish.profile": ProfileName,
			},
		}},
		Tables: make([]*providerv1.TableMetadata, 0, len(generatedTables)),
	}
	for _, definition := range generatedTables {
		metadata.Tables = append(metadata.Tables, metadataTable(definition, options))
	}
	return metadata
}

func metadataTable(definition Table, options MetadataOptions) *providerv1.TableMetadata {
	updatable := false
	table := &providerv1.TableMetadata{
		Name:       definition.Name,
		SourceName: definition.SchemaDefinition,
		Namespace:  Namespace,
		Kind:       providerv1.TableKind_TABLE_KIND_TABLE,
		Updatable:  &updatable,
		Annotation: definition.Annotation,
		Properties: map[string]string{
			"redfish.discovery": string(definition.Discovery.Kind),
		},
		Columns: make([]*providerv1.ColumnMetadata, 0, len(definition.Columns)+1),
	}
	if definition.SchemaFile != "" {
		table.Properties["redfish.schema"] = definition.SchemaFile
	}
	if definition.SchemaDefinition != "" {
		table.Properties["redfish.definition"] = definition.SchemaDefinition
	}
	if definition.Discovery.URI != "" {
		table.Properties["redfish.singleton_uri"] = definition.Discovery.URI
	}
	if definition.Discovery.JSONPointer != "" {
		table.Properties["redfish.collection_pointer"] = definition.Discovery.JSONPointer
	}
	if len(definition.Discovery.Routes) != 0 {
		table.Properties["redfish.traversal_routes"] = strconv.Itoa(len(definition.Discovery.Routes))
	}

	for _, definition := range definition.Columns {
		nullable := definition.Nullable
		searchability := providerv1.ColumnSearchability_COLUMN_SEARCHABILITY_UNSEARCHABLE
		if definition.EqualitySearch {
			searchability = providerv1.ColumnSearchability_COLUMN_SEARCHABILITY_EQUALITY
		}
		properties := map[string]string{
			"redfish.source": string(definition.Source),
		}
		sourceName := definition.Name
		if definition.JSONPointer != "" {
			properties["redfish.json_pointer"] = definition.JSONPointer
			sourceName = definition.JSONPointer
		}
		if definition.ContextKey != "" {
			properties["redfish.context"] = definition.ContextKey
		}
		if len(definition.Mappings) != 0 {
			properties["redfish.variant_mappings"] = strconv.Itoa(len(definition.Mappings))
		}
		table.Columns = append(table.Columns, &providerv1.ColumnMetadata{
			Name:       definition.Name,
			SourceName: sourceName,
			Type:       definition.Type,
			TypeDescriptor: &kublingv1.TypeDescriptor{
				Type: definition.Type,
			},
			NativeType:    definition.NativeType,
			Nullable:      &nullable,
			Updatable:     &updatable,
			Searchability: searchability,
			Annotation:    definition.Annotation,
			Properties:    properties,
		})
	}

	table.Keys = append(table.Keys, &providerv1.KeyMetadata{
		Name:    "UK_" + definition.Name + "_RESOURCE_IDENTITY",
		Kind:    providerv1.KeyKind_KEY_KIND_UNIQUE,
		Columns: append([]string(nil), definition.StableKeyColumns...),
	})
	if !options.AllowUnboundedFanout {
		for _, column := range definition.RoutingColumns {
			table.AccessPatterns = append(table.AccessPatterns, &providerv1.AccessPatternMetadata{
				Name:    "AP_" + definition.Name + "_" + strings.ToUpper(column),
				Columns: []string{column},
			})
		}
	}
	providersdk.MustAddStablePrimaryKey(
		table,
		IdentifierColumn,
		definition.StableKeyColumns...,
	)
	return table
}

// Validate checks invariants shared by the generator and query runtime.
func Validate() error {
	if strings.TrimSpace(ProfileName) == "" || strings.TrimSpace(Namespace) == "" ||
		strings.TrimSpace(DMTFRelease) == "" || strings.TrimSpace(DMTFCommit) == "" {
		return errors.New("generated catalog identity is incomplete")
	}
	if len(generatedTables) == 0 {
		return errors.New("generated catalog contains no tables")
	}
	seenTables := make(map[string]struct{}, len(generatedTables))
	for tableIndex, table := range generatedTables {
		if err := validateTable(table); err != nil {
			return fmt.Errorf("table %d: %w", tableIndex, err)
		}
		key := strings.ToUpper(table.Name)
		if _, exists := seenTables[key]; exists {
			return fmt.Errorf("duplicate table %q", table.Name)
		}
		seenTables[key] = struct{}{}
	}
	return nil
}

func validateTable(table Table) error {
	if strings.TrimSpace(table.Name) == "" {
		return errors.New("name is required")
	}
	switch table.Discovery.Kind {
	case DiscoveryRegistry:
		if table.Discovery.URI != "" || table.Discovery.JSONPointer != "" || len(table.Discovery.Routes) != 0 {
			return errors.New("registry discovery forbids a URI, JSON pointer and traversal routes")
		}
		if table.SchemaFile != "" || table.SchemaDefinition != "" {
			return errors.New("registry discovery forbids a Redfish schema and definition")
		}
	case DiscoverySingleton:
		if strings.TrimSpace(table.SchemaFile) == "" || strings.TrimSpace(table.SchemaDefinition) == "" {
			return errors.New("Redfish schema file and definition are required")
		}
		if table.Discovery.URI == "" || table.Discovery.JSONPointer != "" || len(table.Discovery.Routes) != 0 {
			return errors.New("singleton discovery requires only a URI")
		}
	case DiscoveryCollection:
		if strings.TrimSpace(table.SchemaFile) == "" || strings.TrimSpace(table.SchemaDefinition) == "" {
			return errors.New("Redfish schema file and definition are required")
		}
		if table.Discovery.JSONPointer == "" || table.Discovery.URI != "" || len(table.Discovery.Routes) != 0 {
			return errors.New("collection discovery requires only a JSON pointer")
		}
	case DiscoveryGraph:
		if strings.TrimSpace(table.SchemaFile) == "" || strings.TrimSpace(table.SchemaDefinition) == "" {
			return errors.New("Redfish schema file and definition are required")
		}
		if table.Discovery.JSONPointer != "" || table.Discovery.URI != "" || len(table.Discovery.Routes) == 0 {
			return errors.New("graph discovery requires only traversal routes")
		}
		if err := validateTraversalRoutes(table.Discovery.Routes); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid discovery kind %q", table.Discovery.Kind)
	}
	if len(table.Columns) == 0 || len(table.StableKeyColumns) == 0 {
		return errors.New("columns and stable key are required")
	}
	if table.Discovery.Kind == DiscoveryRegistry {
		if len(table.RoutingColumns) != 0 {
			return errors.New("registry table must not declare routing columns")
		}
	} else if len(table.RoutingColumns) == 0 {
		return errors.New("Redfish table routing columns are required")
	}
	columns := make(map[string]Column, len(table.Columns))
	for _, column := range table.Columns {
		key := strings.ToUpper(strings.TrimSpace(column.Name))
		if key == "" || key == strings.ToUpper(IdentifierColumn) {
			return fmt.Errorf("invalid column name %q", column.Name)
		}
		if _, exists := columns[key]; exists {
			return fmt.Errorf("duplicate column %q", column.Name)
		}
		if column.Type == kublingv1.ValueType_VALUE_TYPE_UNKNOWN || strings.TrimSpace(column.NativeType) == "" {
			return fmt.Errorf("column %q has no concrete type", column.Name)
		}
		switch column.Source {
		case ColumnSourcePayload, ColumnSourceResourceURI:
			if column.JSONPointer == "" && len(column.Mappings) == 0 {
				return fmt.Errorf("column %q requires a JSON pointer", column.Name)
			}
		case ColumnSourceNamespace, ColumnSourceDeviceID, ColumnSourceObservedAt,
			ColumnSourceContext, ColumnSourceVariant, ColumnSourceDeviceCount:
			if column.JSONPointer != "" || len(column.Mappings) != 0 {
				return fmt.Errorf("column %q has an unexpected JSON pointer", column.Name)
			}
		default:
			return fmt.Errorf("column %q has invalid source %q", column.Name, column.Source)
		}
		if column.Source == ColumnSourceContext {
			if strings.TrimSpace(column.ContextKey) == "" {
				return fmt.Errorf("column %q requires a context key", column.Name)
			}
		} else if column.ContextKey != "" {
			return fmt.Errorf("column %q has an unexpected context key", column.Name)
		}
		if table.Discovery.Kind == DiscoveryRegistry {
			if column.Source != ColumnSourceNamespace && column.Source != ColumnSourceDeviceCount {
				return fmt.Errorf("registry column %q has invalid source %q", column.Name, column.Source)
			}
		} else if column.Source == ColumnSourceDeviceCount {
			return fmt.Errorf("Redfish column %q has registry-only source %q", column.Name, column.Source)
		}
		seenMappings := make(map[string]struct{}, len(column.Mappings))
		for _, mapping := range column.Mappings {
			if mapping.BranchID == "" || (mapping.Null == (mapping.JSONPointer != "")) {
				return fmt.Errorf("column %q has an invalid branch mapping", column.Name)
			}
			if _, exists := seenMappings[mapping.BranchID]; exists {
				return fmt.Errorf("column %q repeats branch mapping %q", column.Name, mapping.BranchID)
			}
			seenMappings[mapping.BranchID] = struct{}{}
			if err := validatePredicate(mapping.Predicate); err != nil {
				return fmt.Errorf("column %q mapping %q: %w", column.Name, mapping.BranchID, err)
			}
		}
		columns[key] = column
	}
	for _, name := range table.StableKeyColumns {
		column, exists := columns[strings.ToUpper(name)]
		if !exists {
			return fmt.Errorf("stable-key column %q does not exist", name)
		}
		if column.Nullable {
			return fmt.Errorf("stable-key column %q is nullable", name)
		}
	}
	seenKeyColumns := make(map[string]struct{}, len(table.StableKeyColumns))
	for _, name := range table.StableKeyColumns {
		key := strings.ToUpper(name)
		if _, exists := seenKeyColumns[key]; exists {
			return errors.New("stable-key columns are duplicated")
		}
		seenKeyColumns[key] = struct{}{}
	}
	for _, name := range table.RoutingColumns {
		column, exists := columns[strings.ToUpper(name)]
		if !exists {
			return fmt.Errorf("routing column %q does not exist", name)
		}
		if !column.EqualitySearch {
			return fmt.Errorf("routing column %q is not equality searchable", name)
		}
	}
	return nil
}

func cloneTable(table Table) Table {
	table.Columns = append([]Column(nil), table.Columns...)
	for index := range table.Columns {
		table.Columns[index].Mappings = cloneColumnMappings(table.Columns[index].Mappings)
	}
	table.StableKeyColumns = append([]string(nil), table.StableKeyColumns...)
	table.RoutingColumns = append([]string(nil), table.RoutingColumns...)
	table.Discovery.Routes = cloneTraversalRoutes(table.Discovery.Routes)
	return table
}

func validateTraversalRoutes(routes []TraversalRoute) error {
	seenBranches := make(map[string]struct{})
	for routeIndex, route := range routes {
		if route.Mode != TraversalUnion && route.Mode != TraversalFallback {
			return fmt.Errorf("traversal route %d has invalid mode %q", routeIndex, route.Mode)
		}
		if len(route.Branches) == 0 {
			return fmt.Errorf("traversal route %d has no branches", routeIndex)
		}
		if err := validateTraversalSteps(route.Prefix); err != nil {
			return fmt.Errorf("traversal route %d prefix: %w", routeIndex, err)
		}
		for branchIndex, branch := range route.Branches {
			if branch.ID == "" || len(branch.Steps) == 0 {
				return fmt.Errorf("traversal route %d branch %d is incomplete", routeIndex, branchIndex)
			}
			if _, exists := seenBranches[branch.ID]; exists {
				return fmt.Errorf("duplicate traversal branch %q", branch.ID)
			}
			seenBranches[branch.ID] = struct{}{}
			if err := validateTraversalSteps(branch.Steps); err != nil {
				return fmt.Errorf("traversal branch %q: %w", branch.ID, err)
			}
		}
	}
	return nil
}

func validateTraversalSteps(steps []TraversalStep) error {
	for index, step := range steps {
		switch step.Kind {
		case TraversalSingleton, TraversalCollection, TraversalCollectionLinks,
			TraversalLinked, TraversalLinks, TraversalEmbedded:
		default:
			return fmt.Errorf("step %d has invalid kind %q", index, step.Kind)
		}
		if step.JSONPointer == "" || step.SchemaFile == "" || step.SchemaDefinition == "" {
			return fmt.Errorf("step %d is incomplete", index)
		}
		if err := validatePredicate(step.Predicate); err != nil {
			return fmt.Errorf("step %d: %w", index, err)
		}
	}
	return nil
}

func validatePredicate(predicate *Predicate) error {
	if predicate == nil {
		return nil
	}
	if predicate.JSONPointer == "" || predicate.Equals == "" {
		return errors.New("predicate requires pointer and equals")
	}
	return nil
}

func cloneColumnMappings(input []ColumnMapping) []ColumnMapping {
	result := append([]ColumnMapping(nil), input...)
	for index := range result {
		if result[index].Predicate != nil {
			copy := *result[index].Predicate
			result[index].Predicate = &copy
		}
	}
	return result
}

func cloneTraversalRoutes(input []TraversalRoute) []TraversalRoute {
	result := append([]TraversalRoute(nil), input...)
	for routeIndex := range result {
		result[routeIndex].Prefix = cloneTraversalSteps(result[routeIndex].Prefix)
		result[routeIndex].Branches = append([]TraversalBranch(nil), result[routeIndex].Branches...)
		for branchIndex := range result[routeIndex].Branches {
			result[routeIndex].Branches[branchIndex].Steps = cloneTraversalSteps(result[routeIndex].Branches[branchIndex].Steps)
		}
	}
	return result
}

func cloneTraversalSteps(input []TraversalStep) []TraversalStep {
	result := append([]TraversalStep(nil), input...)
	for index := range result {
		if result[index].Predicate != nil {
			copy := *result[index].Predicate
			result[index].Predicate = &copy
		}
	}
	return result
}
