package config

import (
	"strings"

	"k8s.io/utils/strings/slices"
)

// OperatorConfig holds operator-wide config that is used across all objects
type OperatorConfig struct {
	DisableImageValidation bool     `json:"disableImageValidation"`
	ImageName              string   `json:"imageName"`
	ImageTag               string   `json:"imageTag"`
	ImageDigest            string   `json:"imageDigest"`
	AllowedTags            []string `json:"allowedTags"`
	AllowedImages          []string `json:"allowedImages"`
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
		ImageName:     o.ImageName,
		ImageTag:      o.ImageTag,
		ImageDigest:   o.ImageDigest,
		AllowedTags:   slices.Clone(o.AllowedTags),
		AllowedImages: slices.Clone(o.AllowedImages),
	}
}
