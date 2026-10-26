// Package semanticmodel generates the provider-owned source-local semantic
// fragment from the same metadata used by GetSchema.
package semanticmodel

import (
	"fmt"
	"strings"
	"unicode"

	kublingv1 "github.com/kubling-community/kubling-grpc/sdk-go/kubling/v1"
	"github.com/kubling-community/kubling-provider-redfish/internal/catalog"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	"gopkg.in/yaml.v3"
)

const (
	Name      = "redfish-inventory"
	Namespace = "redfish"
	Version   = "1.0.0"
)

type document struct {
	Format        string           `yaml:"format"`
	SchemaVersion int              `yaml:"schemaVersion"`
	Kind          string           `yaml:"kind"`
	Metadata      documentMetadata `yaml:"metadata"`
	Spec          documentSpec     `yaml:"spec"`
}

type documentMetadata struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
	Version   string `yaml:"version"`
}

type documentSpec struct {
	Entities      []entity       `yaml:"entities"`
	Relationships []relationship `yaml:"relationships"`
}

type entity struct {
	ID              string              `yaml:"id"`
	Name            string              `yaml:"name"`
	Description     string              `yaml:"description"`
	Binding         binding             `yaml:"binding"`
	Properties      []property          `yaml:"properties"`
	Identities      []identity          `yaml:"identities"`
	propertyByField map[string]property `yaml:"-"`
}

type binding struct {
	Relation string `yaml:"relation,omitempty"`
	Field    string `yaml:"field,omitempty"`
}

type property struct {
	ID          string  `yaml:"id"`
	Name        string  `yaml:"name"`
	Description string  `yaml:"description,omitempty"`
	Type        string  `yaml:"type"`
	Binding     binding `yaml:"binding"`
}

type identity struct {
	ID         string   `yaml:"id"`
	Properties []string `yaml:"properties"`
}

type relationship struct {
	ID          string          `yaml:"id"`
	Name        string          `yaml:"name"`
	Description string          `yaml:"description"`
	From        string          `yaml:"from"`
	To          string          `yaml:"to"`
	Cardinality string          `yaml:"cardinality"`
	Joins       []joinCondition `yaml:"joins"`
}

type joinCondition struct {
	Left  string `yaml:"left"`
	Right string `yaml:"right"`
}

type entityProfile struct {
	ID   string
	Name string
}

var entityProfiles = map[string]entityProfile{
	"NAMESPACES":                  {ID: "Namespace", Name: "Namespace"},
	"DEVICES":                     {ID: "Device", Name: "Device"},
	"SYSTEMS":                     {ID: "System", Name: "System"},
	"PROCESSORS":                  {ID: "Processor", Name: "Processor"},
	"MEMORY_MODULES":              {ID: "MemoryModule", Name: "Memory module"},
	"STORAGE_SUBSYSTEMS":          {ID: "StorageSubsystem", Name: "Storage subsystem"},
	"STORAGE_CONTROLLERS":         {ID: "StorageController", Name: "Storage controller"},
	"DRIVES":                      {ID: "Drive", Name: "Drive"},
	"ETHERNET_INTERFACES":         {ID: "EthernetInterface", Name: "Ethernet interface"},
	"SOFTWARE_INVENTORY":          {ID: "SoftwareInventoryItem", Name: "Software inventory item"},
	"CHASSIS":                     {ID: "Chassis", Name: "Chassis"},
	"MANAGERS":                    {ID: "Manager", Name: "Manager"},
	"SYSTEM_CHASSIS":              {ID: "SystemChassisLink", Name: "System-to-chassis link"},
	"SYSTEM_MANAGERS":             {ID: "SystemManagerLink", Name: "System-to-manager link"},
	"STORAGE_DRIVES":              {ID: "StorageDriveLink", Name: "Storage-to-drive link"},
	"CHASSIS_DRIVES":              {ID: "ChassisDriveLink", Name: "Chassis-to-drive link"},
	"SOFTWARE_INVENTORY_TARGETS":  {ID: "SoftwareInventoryTarget", Name: "Software inventory target"},
	"POWER_SUBSYSTEMS":            {ID: "PowerSubsystem", Name: "Power subsystem"},
	"CHASSIS_ENVIRONMENT_METRICS": {ID: "ChassisEnvironmentMetrics", Name: "Chassis environment metrics"},
	"POWER_SUPPLIES":              {ID: "PowerSupply", Name: "Power supply"},
	"TEMPERATURES":                {ID: "Temperature", Name: "Temperature"},
	"FANS":                        {ID: "Fan", Name: "Fan"},
}

type fieldJoin struct {
	Left  string
	Right string
}

type relationshipDefinition struct {
	ID          string
	Name        string
	Description string
	FromTable   string
	ToTable     string
	Cardinality string
	Joins       []fieldJoin
}

// Generate builds deterministic source-local semantics from the canonical
// generated catalog. Relationship definitions remain explicit because table
// discovery alone cannot prove domain direction or cardinality.
func Generate() ([]byte, error) {
	if err := catalog.Validate(); err != nil {
		return nil, fmt.Errorf("validate Redfish catalog: %w", err)
	}
	metadata := catalog.Metadata(catalog.MetadataOptions{})
	entities := make([]entity, 0, len(metadata.GetTables()))
	entitiesByTable := make(map[string]entity, len(metadata.GetTables()))
	seenProfiles := make(map[string]struct{}, len(metadata.GetTables()))

	for _, table := range metadata.GetTables() {
		profile, exists := entityProfiles[table.GetName()]
		if !exists {
			return nil, fmt.Errorf("Redfish semantic profile is missing table %q", table.GetName())
		}
		candidate, err := buildEntity(table, profile)
		if err != nil {
			return nil, err
		}
		entities = append(entities, candidate)
		entitiesByTable[table.GetName()] = candidate
		seenProfiles[table.GetName()] = struct{}{}
	}
	if len(seenProfiles) != len(entityProfiles) {
		return nil, fmt.Errorf(
			"Redfish semantic profile defines %d tables but metadata exposes %d",
			len(entityProfiles),
			len(seenProfiles),
		)
	}

	relationships, err := buildRelationships(entitiesByTable)
	if err != nil {
		return nil, err
	}
	document := document{
		Format:        "kubling-semantic",
		SchemaVersion: 1,
		Kind:          "fragment",
		Metadata: documentMetadata{
			Name:      Name,
			Namespace: Namespace,
			Version:   Version,
		},
		Spec: documentSpec{
			Entities:      entities,
			Relationships: relationships,
		},
	}
	body, err := yaml.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("marshal Redfish semantic fragment: %w", err)
	}
	return append(
		[]byte("# Code generated by redfish-semantic-gen from internal/catalog; DO NOT EDIT.\n"),
		body...,
	), nil
}

func buildEntity(table *providerv1.TableMetadata, profile entityProfile) (entity, error) {
	properties := make([]property, 0, len(table.GetColumns())-1)
	propertyByField := make(map[string]property, len(table.GetColumns())-1)
	var stableKey []string
	for _, column := range table.GetColumns() {
		if column.GetName() == catalog.IdentifierColumn {
			if len(stableKey) != 0 {
				return entity{}, fmt.Errorf("Redfish semantic entity %s has multiple stable identities", profile.ID)
			}
			stableKey = append([]string(nil), column.GetStableKey().GetColumns()...)
			continue
		}
		if _, exists := propertyByField[column.GetName()]; exists {
			return entity{}, fmt.Errorf(
				"Redfish semantic entity %s has duplicate field %q",
				profile.ID,
				column.GetName(),
			)
		}
		propertyType, err := semanticType(column)
		if err != nil {
			return entity{}, fmt.Errorf("%s.%s: %w", table.GetName(), column.GetName(), err)
		}
		candidate := property{
			ID:          lowerCamel(column.GetName()),
			Name:        displayName(column.GetName()),
			Description: column.GetAnnotation(),
			Type:        propertyType,
			Binding: binding{
				Field: column.GetName(),
			},
		}
		properties = append(properties, candidate)
		propertyByField[column.GetName()] = candidate
	}
	if len(stableKey) == 0 {
		return entity{}, fmt.Errorf("Redfish semantic entity %s has no stable identity", profile.ID)
	}
	identityProperties := make([]string, 0, len(stableKey))
	for _, field := range stableKey {
		candidate, exists := propertyByField[field]
		if !exists {
			return entity{}, fmt.Errorf(
				"Redfish semantic identity %s references unknown field %q",
				profile.ID,
				field,
			)
		}
		identityProperties = append(identityProperties, candidate.ID)
	}

	return entity{
		ID:          profile.ID,
		Name:        profile.Name,
		Description: table.GetAnnotation(),
		Binding: binding{
			Relation: table.GetName(),
		},
		Properties: properties,
		Identities: []identity{{
			ID:         lowerFirst(profile.ID) + "Identity",
			Properties: identityProperties,
		}},
		propertyByField: propertyByField,
	}, nil
}

func semanticType(column *providerv1.ColumnMetadata) (string, error) {
	if column.GetType() == kublingv1.ValueType_VALUE_TYPE_ARRAY {
		descriptor := column.GetTypeDescriptor()
		if descriptor.GetType() != kublingv1.ValueType_VALUE_TYPE_ARRAY || descriptor.GetElementType() == nil {
			return "", fmt.Errorf("ARRAY column has no element type")
		}
		element, err := scalarSemanticType(descriptor.GetElementType().GetType())
		if err != nil {
			return "", err
		}
		return element + "[]", nil
	}
	return scalarSemanticType(column.GetType())
}

func scalarSemanticType(valueType kublingv1.ValueType) (string, error) {
	switch valueType {
	case kublingv1.ValueType_VALUE_TYPE_STRING:
		return "string", nil
	case kublingv1.ValueType_VALUE_TYPE_BOOLEAN:
		return "boolean", nil
	case kublingv1.ValueType_VALUE_TYPE_INTEGER:
		return "integer", nil
	case kublingv1.ValueType_VALUE_TYPE_LONG:
		return "long", nil
	case kublingv1.ValueType_VALUE_TYPE_BIGINTEGER:
		return "biginteger", nil
	case kublingv1.ValueType_VALUE_TYPE_DOUBLE:
		return "double", nil
	case kublingv1.ValueType_VALUE_TYPE_TIMESTAMP:
		return "timestamp", nil
	case kublingv1.ValueType_VALUE_TYPE_JSON:
		return "json", nil
	default:
		return "", fmt.Errorf("unsupported semantic value type %s", valueType)
	}
}

func buildRelationships(entities map[string]entity) ([]relationship, error) {
	definitions := relationshipDefinitions()
	result := make([]relationship, 0, len(definitions))
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if _, exists := seen[definition.ID]; exists {
			return nil, fmt.Errorf("duplicate Redfish semantic relationship %q", definition.ID)
		}
		seen[definition.ID] = struct{}{}
		from, fromExists := entities[definition.FromTable]
		to, toExists := entities[definition.ToTable]
		if !fromExists || !toExists {
			return nil, fmt.Errorf("relationship %s references an unknown entity", definition.ID)
		}
		if definition.Cardinality != "oneToOne" && definition.Cardinality != "oneToMany" &&
			definition.Cardinality != "manyToOne" && definition.Cardinality != "manyToMany" {
			return nil, fmt.Errorf("relationship %s has invalid cardinality %q", definition.ID, definition.Cardinality)
		}
		if len(definition.Joins) == 0 {
			return nil, fmt.Errorf("relationship %s has no exact join", definition.ID)
		}
		joins := make([]joinCondition, 0, len(definition.Joins))
		for _, candidate := range definition.Joins {
			left, leftExists := from.propertyByField[candidate.Left]
			right, rightExists := to.propertyByField[candidate.Right]
			if !leftExists || !rightExists {
				return nil, fmt.Errorf(
					"relationship %s references unknown fields %s.%s or %s.%s",
					definition.ID,
					definition.FromTable,
					candidate.Left,
					definition.ToTable,
					candidate.Right,
				)
			}
			if left.Type != right.Type {
				return nil, fmt.Errorf(
					"relationship %s joins incompatible types %s and %s",
					definition.ID,
					left.Type,
					right.Type,
				)
			}
			joins = append(joins, joinCondition{
				Left:  from.ID + "." + left.ID,
				Right: to.ID + "." + right.ID,
			})
		}
		result = append(result, relationship{
			ID:          definition.ID,
			Name:        definition.Name,
			Description: definition.Description,
			From:        from.ID,
			To:          to.ID,
			Cardinality: definition.Cardinality,
			Joins:       joins,
		})
	}
	return result, nil
}

func relationshipDefinitions() []relationshipDefinition {
	deviceJoins := func() []fieldJoin {
		return []fieldJoin{
			{Left: "namespace", Right: "namespace"},
			{Left: "device_id", Right: "device_id"},
		}
	}
	resourceJoins := func(leftURI string, rightURI string) []fieldJoin {
		return append(deviceJoins(), fieldJoin{Left: leftURI, Right: rightURI})
	}
	return []relationshipDefinition{
		{
			ID: "NamespaceContainsDevices", Name: "Namespace contains devices",
			Description: "A configured namespace groups the Redfish services registered under that namespace.",
			FromTable:   "NAMESPACES", ToTable: "DEVICES", Cardinality: "oneToMany",
			Joins: []fieldJoin{{Left: "namespace", Right: "namespace"}},
		},
		{
			ID: "DeviceHasSystems", Name: "Device has systems",
			Description: "A configured Redfish service exposes computer systems on the same device.",
			FromTable:   "DEVICES", ToTable: "SYSTEMS", Cardinality: "oneToMany", Joins: deviceJoins(),
		},
		{
			ID: "DeviceHasChassis", Name: "Device has chassis",
			Description: "A configured Redfish service exposes chassis on the same device.",
			FromTable:   "DEVICES", ToTable: "CHASSIS", Cardinality: "oneToMany", Joins: deviceJoins(),
		},
		{
			ID: "DeviceHasManagers", Name: "Device has managers",
			Description: "A configured Redfish service exposes managers on the same device.",
			FromTable:   "DEVICES", ToTable: "MANAGERS", Cardinality: "oneToMany", Joins: deviceJoins(),
		},
		{
			ID: "SystemHasProcessors", Name: "System has processors",
			Description: "A computer system contains processor resources discovered below its resource URI.",
			FromTable:   "SYSTEMS", ToTable: "PROCESSORS", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "system_uri"),
		},
		{
			ID: "SystemHasMemoryModules", Name: "System has memory modules",
			Description: "A computer system contains memory resources discovered below its resource URI.",
			FromTable:   "SYSTEMS", ToTable: "MEMORY_MODULES", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "system_uri"),
		},
		{
			ID: "SystemHasStorageSubsystems", Name: "System has storage subsystems",
			Description: "A computer system contains storage subsystems discovered below its resource URI.",
			FromTable:   "SYSTEMS", ToTable: "STORAGE_SUBSYSTEMS", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "system_uri"),
		},
		{
			ID: "SystemHasStorageControllers", Name: "System has storage controllers",
			Description: "A computer system contains storage controllers discovered below its resource URI.",
			FromTable:   "SYSTEMS", ToTable: "STORAGE_CONTROLLERS", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "system_uri"),
		},
		{
			ID: "StorageSubsystemHasControllers", Name: "Storage subsystem has controllers",
			Description: "A storage subsystem contains modern or legacy controller resources.",
			FromTable:   "STORAGE_SUBSYSTEMS", ToTable: "STORAGE_CONTROLLERS", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "storage_uri"),
		},
		{
			ID: "SystemHasEthernetInterfaces", Name: "System has Ethernet interfaces",
			Description: "A computer system owns Ethernet interfaces whose captured owner URI matches the system URI.",
			FromTable:   "SYSTEMS", ToTable: "ETHERNET_INTERFACES", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "owner_uri"),
		},
		{
			ID: "ManagerHasEthernetInterfaces", Name: "Manager has Ethernet interfaces",
			Description: "A manager owns Ethernet interfaces whose captured owner URI matches the manager URI.",
			FromTable:   "MANAGERS", ToTable: "ETHERNET_INTERFACES", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "owner_uri"),
		},
		{
			ID: "SystemHasChassisLinks", Name: "System has chassis links",
			Description: "A computer system is the source of materialized system-to-chassis link rows.",
			FromTable:   "SYSTEMS", ToTable: "SYSTEM_CHASSIS", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "source_uri"),
		},
		{
			ID: "ChassisHasSystemLinks", Name: "Chassis has system links",
			Description: "A chassis is the target of materialized system-to-chassis link rows.",
			FromTable:   "CHASSIS", ToTable: "SYSTEM_CHASSIS", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "target_uri"),
		},
		{
			ID: "SystemHasManagerLinks", Name: "System has manager links",
			Description: "A computer system is the source of materialized system-to-manager link rows.",
			FromTable:   "SYSTEMS", ToTable: "SYSTEM_MANAGERS", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "source_uri"),
		},
		{
			ID: "ManagerHasSystemLinks", Name: "Manager has system links",
			Description: "A manager is the target of materialized system-to-manager link rows.",
			FromTable:   "MANAGERS", ToTable: "SYSTEM_MANAGERS", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "target_uri"),
		},
		{
			ID: "StorageSubsystemHasDriveLinks", Name: "Storage subsystem has drive links",
			Description: "A storage subsystem is the source of materialized storage-to-drive link rows.",
			FromTable:   "STORAGE_SUBSYSTEMS", ToTable: "STORAGE_DRIVES", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "source_uri"),
		},
		{
			ID: "DriveHasStorageLinks", Name: "Drive has storage links",
			Description: "A drive is the target of materialized storage-to-drive link rows.",
			FromTable:   "DRIVES", ToTable: "STORAGE_DRIVES", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "target_uri"),
		},
		{
			ID: "ChassisHasDriveLinks", Name: "Chassis has drive links",
			Description: "A chassis is the source of materialized chassis-to-drive link rows.",
			FromTable:   "CHASSIS", ToTable: "CHASSIS_DRIVES", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "source_uri"),
		},
		{
			ID: "DriveHasChassisLinks", Name: "Drive has chassis links",
			Description: "A drive is the target of materialized chassis-to-drive link rows.",
			FromTable:   "DRIVES", ToTable: "CHASSIS_DRIVES", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "target_uri"),
		},
		{
			ID: "SoftwareInventoryHasTargetLinks", Name: "Software inventory has target links",
			Description: "A software inventory item is the source of materialized affected-resource link rows.",
			FromTable:   "SOFTWARE_INVENTORY", ToTable: "SOFTWARE_INVENTORY_TARGETS", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "source_uri"),
		},
		{
			ID: "ChassisHasPowerSubsystem", Name: "Chassis has power subsystem",
			Description: "A chassis exposes one selected modern power subsystem or legacy power resource.",
			FromTable:   "CHASSIS", ToTable: "POWER_SUBSYSTEMS", Cardinality: "oneToOne", Joins: resourceJoins("resource_uri", "chassis_uri"),
		},
		{
			ID: "ChassisHasEnvironmentMetrics", Name: "Chassis has environment metrics",
			Description: "A chassis exposes current modern environment metrics or legacy power-control observations.",
			FromTable:   "CHASSIS", ToTable: "CHASSIS_ENVIRONMENT_METRICS", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "chassis_uri"),
		},
		{
			ID: "ChassisHasPowerSupplies", Name: "Chassis has power supplies",
			Description: "A chassis contains modern or legacy power-supply resources.",
			FromTable:   "CHASSIS", ToTable: "POWER_SUPPLIES", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "chassis_uri"),
		},
		{
			ID: "ChassisHasTemperatures", Name: "Chassis has temperatures",
			Description: "A chassis contains modern or legacy temperature observations.",
			FromTable:   "CHASSIS", ToTable: "TEMPERATURES", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "chassis_uri"),
		},
		{
			ID: "ChassisHasFans", Name: "Chassis has fans",
			Description: "A chassis contains modern or legacy fan resources.",
			FromTable:   "CHASSIS", ToTable: "FANS", Cardinality: "oneToMany", Joins: resourceJoins("resource_uri", "chassis_uri"),
		},
	}
}

func lowerCamel(value string) string {
	parts := strings.Split(value, "_")
	for index := range parts {
		if index == 0 {
			parts[index] = strings.ToLower(parts[index])
			continue
		}
		parts[index] = upperFirst(strings.ToLower(parts[index]))
	}
	return strings.Join(parts, "")
}

func displayName(value string) string {
	aliases := map[string]string{
		"id":    "ID",
		"kwh":   "kWh",
		"odata": "OData",
		"rpm":   "RPM",
		"sku":   "SKU",
		"uri":   "URI",
		"uuid":  "UUID",
	}
	parts := strings.Split(value, "_")
	for index, part := range parts {
		if alias, exists := aliases[part]; exists {
			parts[index] = alias
			continue
		}
		if index == 0 {
			parts[index] = upperFirst(part)
		} else {
			parts[index] = part
		}
	}
	return strings.Join(parts, " ")
}

func lowerFirst(value string) string {
	if value == "" {
		return value
	}
	runes := []rune(value)
	runes[0] = unicode.ToLower(runes[0])
	return string(runes)
}

func upperFirst(value string) string {
	if value == "" {
		return value
	}
	runes := []rune(value)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}
