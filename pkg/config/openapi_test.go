package config

import (
	"encoding/json"
	"path/filepath"
	"testing"

	openapi_v2 "github.com/google/gnostic-models/openapiv2"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/kube-openapi/pkg/util/proto"
	prototesting "k8s.io/kube-openapi/pkg/util/proto/testing"
	"k8s.io/kubectl/pkg/util/openapi"
	openapitesting "k8s.io/kubectl/pkg/util/openapi/testing"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

// patchedGVKs are the kinds the operator applies patches to, which are the only
// kinds LookupResource is ever called for.
var patchedGVKs = []schema.GroupVersionKind{
	{Group: "", Version: "v1", Kind: "ServiceAccount"},
	{Group: "", Version: "v1", Kind: "Service"},
	{Group: "apps", Version: "v1", Kind: "Deployment"},
	{Group: "batch", Version: "v1", Kind: "Job"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "Role"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "RoleBinding"},
	{Group: "policy", Version: "v1", Kind: "PodDisruptionBudget"},
}

// TestModelsOnlyResourcesMatchesKubectl is the load-bearing test for dropping
// the raw OpenAPI document: the models-only implementation must resolve every
// kind the operator patches to exactly the same model kubectl's implementation
// resolves it to. If these diverge, strategic merge patches would silently
// change behavior (merge keys would be looked up against the wrong schema, or
// not at all).
func TestModelsOnlyResourcesMatchesKubectl(t *testing.T) {
	for _, swagger := range []string{"swagger.1.26.3.json", "swagger.1.30.2.json"} {
		t.Run(swagger, func(t *testing.T) {
			path := filepath.Join("testdata", swagger)

			doc, err := (&prototesting.Fake{Path: path}).OpenAPISchema()
			require.NoError(t, err)
			reference, err := openapi.NewOpenAPIData(doc)
			require.NoError(t, err)

			got, err := NewModelsOnlyResourcesGetter(&prototesting.Fake{Path: path}).OpenAPISchema()
			require.NoError(t, err)

			for _, gvk := range patchedGVKs {
				t.Run(gvk.Kind, func(t *testing.T) {
					want := reference.LookupResource(gvk)
					actual := got.LookupResource(gvk)

					// Fixtures are pinned to specific Kubernetes versions and
					// don't all describe every kind at the group-version the
					// operator uses today (1.26.3 has PodDisruptionBudget only
					// at policy/v1beta1). What must hold is that the two
					// implementations agree, present or not.
					if want == nil {
						require.Nil(t, actual, "models-only resolved %s that kubectl did not", gvk)
						t.Skipf("fixture does not describe %s", gvk)
					}
					require.NotNil(t, actual, "models-only lookup lost %s", gvk)

					// Compare the model's path and its sorted field names.
					// proto.Kind.GetName() is deliberately not used: it renders
					// an unsorted map iteration and is unstable between parses.
					require.Equal(t, want.GetPath().String(), actual.GetPath().String())

					wantKind, ok := want.(*proto.Kind)
					require.True(t, ok, "expected %s to resolve to a Kind", gvk)
					actualKind, ok := actual.(*proto.Kind)
					require.True(t, ok, "expected %s to resolve to a Kind", gvk)
					require.Equal(t, wantKind.Keys(), actualKind.Keys())
				})
			}
		})
	}
}

// TestModelsOnlyResourcesPatchesIdentically is the behavioral counterpart to
// the lookup test. A strategic merge patch that targets one container by name
// only merges (rather than replaces the whole list) if the schema supplies the
// "name" merge key for spec.template.spec.containers. Applying the same patch
// through kubectl's implementation and the models-only one must therefore
// produce byte-identical output.
func TestModelsOnlyResourcesPatchesIdentically(t *testing.T) {
	path := filepath.Join("testdata", "swagger.1.30.2.json")

	patches := []v1alpha1.Patch{{
		Kind: "Deployment",
		Patch: json.RawMessage(`
spec:
  template:
    spec:
      containers:
      - name: spicedb
        image: patched-image:v1`),
	}}

	deployment := func() *applyappsv1.DeploymentApplyConfiguration {
		return applyappsv1.Deployment("test", "test").
			WithSpec(applyappsv1.DeploymentSpec().
				WithTemplate(applycorev1.PodTemplateSpec().
					WithSpec(applycorev1.PodSpec().
						WithContainers(
							applycorev1.Container().WithName("spicedb").WithImage("original:v0"),
							applycorev1.Container().WithName("sidecar").WithImage("sidecar:v0"),
						))))
	}

	getters := map[string]openapi.OpenAPIResourcesGetter{
		"kubectl": StaticResourcesGetter{Resources: openapitesting.NewFakeResources(path)},
		"models-only": func() openapi.OpenAPIResourcesGetter {
			return NewModelsOnlyResourcesGetter(&prototesting.Fake{Path: path})
		}(),
	}

	results := make(map[string][]byte, len(getters))
	for name, getter := range getters {
		out := deployment()
		count, patched, err := ApplyPatches(deployment(), out, patches, getter)
		require.NoError(t, err)
		require.Equal(t, 1, count)
		require.True(t, patched)

		encoded, err := json.Marshal(out)
		require.NoError(t, err)
		results[name] = encoded

		// The merge key must have been honored: the sidecar survives and the
		// targeted container is updated in place.
		require.Contains(t, string(encoded), "patched-image:v1")
		require.Contains(t, string(encoded), "sidecar:v0",
			"sidecar was dropped, so the containers list was replaced instead of merged")
	}

	require.JSONEq(t, string(results["kubectl"]), string(results["models-only"]))
}

func TestModelsOnlyResourcesUnknownKind(t *testing.T) {
	got, err := NewModelsOnlyResourcesGetter(&prototesting.Fake{
		Path: filepath.Join("testdata", "swagger.1.30.2.json"),
	}).OpenAPISchema()
	require.NoError(t, err)

	require.Nil(t, got.LookupResource(schema.GroupVersionKind{
		Group: "authzed.com", Version: "v1alpha1", Kind: "NotAThing",
	}))
}

// TestModelsOnlyResourcesGetterMemoizes guards the property the memory saving
// depends on: the document is fetched and parsed once, no matter how many
// patches are applied over the operator's lifetime.
func TestModelsOnlyResourcesGetterMemoizes(t *testing.T) {
	counter := &countingSchemaClient{
		Fake: prototesting.Fake{Path: filepath.Join("testdata", "swagger.1.30.2.json")},
	}
	getter := NewModelsOnlyResourcesGetter(counter)

	first, err := getter.OpenAPISchema()
	require.NoError(t, err)
	second, err := getter.OpenAPISchema()
	require.NoError(t, err)

	require.Same(t, first, second)
	require.Equal(t, 1, counter.calls)
}

type countingSchemaClient struct {
	prototesting.Fake
	calls int
}

func (c *countingSchemaClient) OpenAPISchema() (*openapi_v2.Document, error) {
	c.calls++
	return c.Fake.OpenAPISchema()
}
