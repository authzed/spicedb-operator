package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cespare/xxhash/v2"
	"golang.org/x/exp/maps"
	"golang.org/x/exp/slices"
)

type SpiceDBMigrationState struct {
	Tag       string `json:"tag"`
	Migration string `json:"migration"`
	Phase     string `json:"phase"`
}

func (s SpiceDBMigrationState) String() string {
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("error marshalling state: %w", err).Error()
	}
	return fmt.Sprintf("%x", xxhash.Sum64(b))
}

type SpiceDBDatastoreState struct {
	Tag       string `json:"tag"`
	Datastore string `json:"datastore"`
}

func (s SpiceDBDatastoreState) String() string {
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("error marshalling state: %w", err).Error()
	}
	return fmt.Sprintf("%x", xxhash.Sum64(b))
}

// OperatorConfig holds operator-wide config that is used across all objects
type OperatorConfig struct {
	DisableImageValidation bool     `json:"disableImageValidation"`
	ImageName              string   `json:"imageName,omitempty"`
	ImageTag               string   `json:"imageTag,omitempty"`
	ImageDigest            string   `json:"imageDigest,omitempty"`
	AllowedTags            []string `json:"allowedTags,omitempty"`
	AllowedImages          []string `json:"allowedImages,omitempty"`
	UpdateGraph
}

func NewOperatorConfig() OperatorConfig {
	return OperatorConfig{
		AllowedTags:   make([]string, 0),
		AllowedImages: make([]string, 0),
		UpdateGraph:   NewUpdateGraph(),
	}
}

func (o OperatorConfig) DefaultImage() string {
	if len(o.ImageDigest) > 0 {
		return strings.Join([]string{o.ImageName, o.ImageDigest}, "@")
	}
	if len(o.ImageTag) > 0 {
		return strings.Join([]string{o.ImageName, o.ImageTag}, ":")
	}
	return o.ImageName
}

func (o OperatorConfig) Copy() OperatorConfig {
	return OperatorConfig{
		DisableImageValidation: o.DisableImageValidation,
		ImageName:              o.ImageName,
		ImageTag:               o.ImageTag,
		ImageDigest:            o.ImageDigest,
		AllowedTags:            slices.Clone(o.AllowedTags),
		AllowedImages:          slices.Clone(o.AllowedImages),
		UpdateGraph:            o.UpdateGraph.Copy(),
	}
}

// UpdateGraph holds a graph of required update edges
type UpdateGraph struct {
	HeadMigrations map[string]string                `json:"headMigrations,omitempty"`
	RequiredEdges  map[string]string                `json:"requiredEdges,omitempty"`
	Nodes          map[string]SpiceDBMigrationState `json:"nodes,omitempty"`
}

func NewUpdateGraph() UpdateGraph {
	return UpdateGraph{
		HeadMigrations: make(map[string]string, 0),
		RequiredEdges:  make(map[string]string, 0),
		Nodes:          make(map[string]SpiceDBMigrationState, 0),
	}
}

func (o UpdateGraph) GetHeadMigration(state SpiceDBDatastoreState) string {
	if m, ok := o.HeadMigrations[state.String()]; ok {
		return m
	}
	return "head"
}

func (o UpdateGraph) GetRequiredEdge(state SpiceDBMigrationState) (*SpiceDBMigrationState, error) {
	requiredEdge, ok := o.RequiredEdges[state.String()]
	if !ok {
		return nil, nil
	}
	requiredNode, ok := o.Nodes[requiredEdge]
	if !ok {
		return nil, fmt.Errorf("required edge defined but not found: %s -> %s", state.String(), requiredEdge)
	}
	return &requiredNode, nil
}

func (o UpdateGraph) AddHeadMigration(state SpiceDBDatastoreState, migrationName string) UpdateGraph {
	o.HeadMigrations[state.String()] = migrationName
	return o
}

func (o UpdateGraph) AddEdge(from, to SpiceDBMigrationState) UpdateGraph {
	o.Nodes[from.String()] = from
	o.Nodes[to.String()] = to
	o.RequiredEdges[from.String()] = to.String()
	return o
}

func (o UpdateGraph) Copy() UpdateGraph {
	return UpdateGraph{
		HeadMigrations: maps.Clone(o.HeadMigrations),
		RequiredEdges:  maps.Clone(o.RequiredEdges),
		Nodes:          maps.Clone(o.Nodes),
	}
}
