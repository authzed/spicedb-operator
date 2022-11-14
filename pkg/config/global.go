package config

import (
	"encoding/json"
	"fmt"

	"github.com/cespare/xxhash/v2"

	"github.com/authzed/spicedb-operator/pkg/updates"
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
	ImageName string `json:"imageName,omitempty"`
	updates.UpdateGraph
}

func NewOperatorConfig() OperatorConfig {
	return OperatorConfig{
		UpdateGraph: updates.UpdateGraph{},
	}
}

func (o OperatorConfig) Copy() OperatorConfig {
	return OperatorConfig{
		ImageName:   o.ImageName,
		UpdateGraph: o.UpdateGraph.Copy(),
	}
}
