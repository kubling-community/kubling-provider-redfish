package main

import "encoding/json"

type upstreamLock struct {
	FormatVersion int          `json:"formatVersion"`
	Repository    string       `json:"repository"`
	Release       string       `json:"release"`
	Commit        string       `json:"commit"`
	License       string       `json:"license"`
	Files         []lockedFile `json:"files"`
}

type lockedFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type profile struct {
	FormatVersion int               `yaml:"formatVersion"`
	Name          string            `yaml:"name"`
	Namespace     string            `yaml:"namespace"`
	ServiceRoot   resourceReference `yaml:"serviceRoot"`
	Tables        []profileTable    `yaml:"tables"`
}

type resourceReference struct {
	Schema     string `yaml:"schema"`
	Definition string `yaml:"definition"`
}

type profileTable struct {
	Name       string            `yaml:"name"`
	Annotation string            `yaml:"annotation"`
	Resource   resourceReference `yaml:"resource"`
	Discovery  profileDiscovery  `yaml:"discovery"`
	StableKey  []string          `yaml:"stableKey"`
	Routing    []string          `yaml:"routing"`
	Columns    []profileColumn   `yaml:"columns"`
}

type profileDiscovery struct {
	Kind    string                  `yaml:"kind"`
	URI     string                  `yaml:"uri"`
	Pointer string                  `yaml:"pointer"`
	Routes  []profileTraversalRoute `yaml:"routes"`
}

type profileColumn struct {
	Name       string `yaml:"name"`
	Source     string `yaml:"source"`
	Pointer    string `yaml:"pointer"`
	Context    string `yaml:"context"`
	Annotation string `yaml:"annotation"`
}

type profileTraversalRoute struct {
	Prefix   []profileTraversalStep   `yaml:"prefix"`
	Mode     string                   `yaml:"mode"`
	Branches []profileTraversalBranch `yaml:"branches"`
}

type profileTraversalBranch struct {
	Name     string                          `yaml:"name"`
	Variant  string                          `yaml:"variant"`
	Steps    []profileTraversalStep          `yaml:"steps"`
	Mappings map[string]profileColumnMapping `yaml:"mappings"`
}

type profileTraversalStep struct {
	Kind     string            `yaml:"kind"`
	Pointer  string            `yaml:"pointer"`
	Resource resourceReference `yaml:"resource"`
	Capture  string            `yaml:"capture"`
	Where    *profilePredicate `yaml:"where"`
}

type profileColumnMapping struct {
	Pointer string            `yaml:"pointer"`
	Null    bool              `yaml:"omit"`
	When    *profilePredicate `yaml:"when"`
}

type profilePredicate struct {
	Pointer string `yaml:"pointer"`
	Equals  string `yaml:"equals"`
}

type schemaDocument struct {
	name string
	root map[string]any
}

type schemaSet struct {
	lock      upstreamLock
	documents map[string]schemaDocument
	locked    map[string]struct{}
}

type generatedCatalog struct {
	ProfileName string
	Namespace   string
	Release     string
	Commit      string
	Tables      []generatedTable
}

type generatedTable struct {
	Name             string
	Annotation       string
	SchemaFile       string
	SchemaDefinition string
	DiscoveryKind    string
	DiscoveryURI     string
	DiscoveryPointer string
	TraversalRoutes  []generatedTraversalRoute
	Columns          []generatedColumn
	StableKey        []string
	Routing          []string
}

type generatedColumn struct {
	Name           string
	SourceConstant string
	Pointer        string
	Context        string
	TypeConstant   string
	NativeType     string
	Nullable       bool
	Searchable     bool
	Annotation     string
	Mappings       []generatedColumnMapping
}

type generatedTraversalRoute struct {
	Prefix   []generatedTraversalStep
	Mode     string
	Branches []generatedTraversalBranch
}

type generatedTraversalBranch struct {
	ID      string
	Variant string
	Steps   []generatedTraversalStep
}

type generatedTraversalStep struct {
	Kind             string
	Pointer          string
	SchemaFile       string
	SchemaDefinition string
	Capture          string
	PredicatePointer string
	PredicateEquals  string
}

type generatedColumnMapping struct {
	BranchID         string
	Pointer          string
	Null             bool
	PredicatePointer string
	PredicateEquals  string
}

type nodeLocation struct {
	file string
	node map[string]any
}

type propertyResolution struct {
	logicalType string
	nativeType  string
	nullable    bool
	description string
}

func decodeJSONObject(contents []byte) (map[string]any, error) {
	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil {
		return nil, err
	}
	return document, nil
}
