package crds

import (
	"embed"

	"k8s.io/client-go/rest"

	libbootstrap "github.com/authzed/controller-idioms/bootstrap"
)

//go:embed *.yaml
var crdFS embed.FS

func BootstrapCRD(restConfig *rest.Config) error {
	return libbootstrap.CRD(restConfig, crdFS, ".")
}
