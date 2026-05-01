package config

import "github.com/authzed/spicedb-operator/pkg/updates"

// OperatorConfig holds operator-wide config that is used across all objects
type OperatorConfig struct {
	ImageName string `json:"imageName,omitempty"`
	updates.UpdateGraph
}

func NewOperatorConfig() OperatorConfig {
	return OperatorConfig{
		UpdateGraph: updates.UpdateGraph{
			Channels: make([]updates.Channel, 0),
		},
	}
}

func (o OperatorConfig) Copy() OperatorConfig {
	return OperatorConfig{
		ImageName:   o.ImageName,
		UpdateGraph: o.UpdateGraph.Copy(),
	}
}
