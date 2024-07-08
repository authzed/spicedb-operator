package databases

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/fluxcd/cli-utils/pkg/kstatus/polling"
	"github.com/fluxcd/pkg/ssa"
	//revive:disable:dot-imports convention is dot-import
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	crClient "sigs.k8s.io/controller-runtime/pkg/client"
)

//go:embed manifests/*.yaml
var datastores embed.FS

type LogicalDatabase struct {
	Engine       string
	DatastoreURI string
	DatabaseName string
	ExtraConfig  map[string]string
}

type Provider interface {
	New(ctx context.Context) *LogicalDatabase
	Cleanup(ctx context.Context, db *LogicalDatabase)
}

func CreateFromManifests(ctx context.Context, namespace, engine string, restConfig *rest.Config, mapper meta.RESTMapper) {
	ssaClient, err := crClient.NewWithWatch(restConfig, crClient.Options{
		Mapper: mapper,
	})
	Expect(err).To(Succeed())
	resourceManager := ssa.NewResourceManager(ssaClient, polling.NewStatusPoller(ssaClient, mapper, polling.Options{}), ssa.Owner{
		Field: "test.authzed.com",
		Group: "test.authzed.com",
	})

	yamlReader, err := datastores.Open(fmt.Sprintf("manifests/%s.yaml", engine))
	Expect(err).To(Succeed())
	DeferCleanup(yamlReader.Close)

	decoder := yaml.NewYAMLToJSONDecoder(yamlReader)
	objs := make([]*unstructured.Unstructured, 0)
	for {
		u := &unstructured.Unstructured{Object: map[string]interface{}{}}
		err := decoder.Decode(&u.Object)
		if errors.Is(err, io.EOF) {
			break
		}
		Expect(err).To(Succeed())
		u.SetNamespace(namespace)
		objs = append(objs, u)
	}
	_, err = resourceManager.ApplyAll(ctx, objs, ssa.DefaultApplyOptions())
	Expect(err).To(Succeed())
	By(fmt.Sprintf("waiting for %s to start..", engine))
	err = resourceManager.Wait(objs, ssa.WaitOptions{
		Interval: 1 * time.Second,
		Timeout:  120 * time.Second,
	})
	Expect(err).To(Succeed())
	By(fmt.Sprintf("%s running", engine))
}
