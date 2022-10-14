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
	DisableImageValidation bool                             `json:"disableImageValidation"`
	ImageName              string                           `json:"imageName"`
	ImageTag               string                           `json:"imageTag"`
	ImageDigest            string                           `json:"imageDigest,omitempty"`
	AllowedTags            []string                         `json:"allowedTags"`
	AllowedImages          []string                         `json:"allowedImages"`
	HeadMigrations         map[string]string                `json:"headMigrations"`
	RequiredEdges          map[string]string                `json:"requiredEdges"`
	Nodes                  map[string]SpiceDBMigrationState `json:"nodes"`
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
		HeadMigrations:         maps.Clone(o.HeadMigrations),
		RequiredEdges:          maps.Clone(o.RequiredEdges),
		Nodes:                  maps.Clone(o.Nodes),
	}
}
