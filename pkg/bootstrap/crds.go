package bootstrap

import (
	"bytes"
	_ "embed"
	"time"

	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

//go:embed crds/authzed.com_authzedenterpriseclusters.yaml
var clusterCRDFile []byte

func BootstrapCRD(restConfig *rest.Config) error {
	var clusterCRD v1.CustomResourceDefinition
	if err := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(clusterCRDFile), 100).Decode(&clusterCRD); err != nil {
		return err
	}
	_, err := envtest.InstallCRDs(restConfig, envtest.CRDInstallOptions{
		CRDs:            []*v1.CustomResourceDefinition{&clusterCRD},
		MaxTime:         5 * time.Minute,
		PollInterval:    200 * time.Millisecond,
		CleanUpAfterUse: false,
	})
	return err
}
