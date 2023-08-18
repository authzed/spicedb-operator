package crds

import (
	"context"
	"embed"

	"k8s.io/client-go/rest"

	libbootstrap "github.com/authzed/controller-idioms/bootstrap"
)

//go:embed *.yaml
var crdFS embed.FS

func BootstrapCRD(ctx context.Context, restConfig *rest.Config) error {
	return libbootstrap.CRDs(ctx, restConfig, crdFS, ".")
}
