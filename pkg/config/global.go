package config

import (
	"strings"

	"golang.org/x/exp/maps"
	"k8s.io/utils/strings/slices"
)

// OperatorConfig holds operator-wide config that is used across all objects
type OperatorConfig struct {
	DisableImageValidation bool              `json:"disableImageValidation"`
	ImageName              string            `json:"imageName"`
	ImageTag               string            `json:"imageTag"`
	ImageDigest            string            `json:"imageDigest,omitempty"`
	AllowedTags            []string          `json:"allowedTags"`
	AllowedImages          []string          `json:"allowedImages"`
	RequiredTagEdges       map[string]string `json:"requiredTagEdges"`
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
		RequiredTagEdges:       maps.Clone(o.RequiredTagEdges),
	}
}
