package config

import (
	"bytes"
	"io"
	"io/ioutil"

	"github.com/cespare/xxhash/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

type OperatorConfig struct {
	ImageName     string               `json:"imageName"`
	ImageTag      string               `json:"imageTag"`
	ImageDigest   string               `json:"imageDigest"`
	AllowedTags   []string             `json:"allowedTags"`
	AllowedImages []string             `json:"allowedImages"`
	LabelSelector metav1.LabelSelector `json:"selector"`
}

func NewOperatorConfig(r io.Reader) (*OperatorConfig, uint64, error) {
	contents, err := ioutil.ReadAll(r)
	if err != nil {
		panic(err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(contents), 100)
	var c OperatorConfig
	if err := decoder.Decode(&c); err != nil {
		return nil, 0, err
	}

	return &c, xxhash.Sum64(contents), nil
}
