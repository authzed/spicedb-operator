package config

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/openapi/openapitest"
	"k8s.io/client-go/openapi3"
	"k8s.io/kube-openapi/pkg/spec3"
	openapitesting "k8s.io/kubectl/pkg/util/openapi/testing"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

func newV3Resolver() *V3PatchMetaResolver {
	return NewV3PatchMetaResolver(openapi3.NewRoot(openapitest.NewEmbeddedFileClient()))
}

// containerMergePatch targets one container by name. Strategic merge only keeps
// the other containers if the schema supplies the "name" merge key for
// spec.template.spec.containers; without it the whole list is replaced. This is
// the property that would break silently if patch metadata resolution
// regressed, so it anchors most of the tests below.
var containerMergePatch = []v1alpha1.Patch{{
	Kind: "Deployment",
	Patch: json.RawMessage(`
spec:
  template:
    spec:
      containers:
      - name: spicedb
        image: patched-image:v1`),
}}

func testDeployment() *applyappsv1.DeploymentApplyConfiguration {
	return applyappsv1.Deployment("test", "test").
		WithSpec(applyappsv1.DeploymentSpec().
			WithTemplate(applycorev1.PodTemplateSpec().
				WithSpec(applycorev1.PodSpec().
					WithContainers(
						applycorev1.Container().WithName("spicedb").WithImage("original:v0"),
						applycorev1.Container().WithName("sidecar").WithImage("sidecar:v0"),
					))))
}

func TestV3PatchMetaResolverLookup(t *testing.T) {
	resolver := newV3Resolver()

	// Limited to the group-versions client-go ships v3 fixtures for.
	for _, gvk := range []schema.GroupVersionKind{
		{Group: "", Version: "v1", Kind: "ServiceAccount"},
		{Group: "", Version: "v1", Kind: "Service"},
		{Group: "apps", Version: "v1", Kind: "Deployment"},
		{Group: "batch", Version: "v1", Kind: "Job"},
	} {
		t.Run(gvk.Kind, func(t *testing.T) {
			meta, err := resolver.LookupPatchMeta(gvk)
			require.NoError(t, err)
			require.NotNil(t, meta)
		})
	}
}

// TestV3PatchMetaResolverUnknownKind covers a kind missing from a group-version
// that does exist, which is the index miss rather than a failed fetch.
func TestV3PatchMetaResolverUnknownKind(t *testing.T) {
	_, err := newV3Resolver().LookupPatchMeta(schema.GroupVersionKind{
		Group: "apps", Version: "v1", Kind: "NotAThing",
	})
	require.ErrorContains(t, err, "no OpenAPI v3 schema describes")
}

func TestV3PatchMetaResolverMissingGroupVersion(t *testing.T) {
	_, err := newV3Resolver().LookupPatchMeta(schema.GroupVersionKind{
		Group: "nonexistent.example.com", Version: "v1", Kind: "Widget",
	})
	require.Error(t, err)
}

// TestV3PatchMetaResolverHonorsMergeKeys is the behavioral test: the v3 schema
// must yield the container merge key, so that patching one container leaves the
// other in place.
func TestV3PatchMetaResolverHonorsMergeKeys(t *testing.T) {
	out := testDeployment()
	count, patched, err := ApplyPatches(testDeployment(), out, containerMergePatch, newV3Resolver())
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.True(t, patched)

	encoded, err := json.Marshal(out)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "patched-image:v1")
	require.Contains(t, string(encoded), "sidecar:v0",
		"sidecar was dropped, so containers were replaced rather than merged by name")
}

// TestV3MatchesV2PatchResults pins the migration: switching the operator from
// OpenAPI v2 to v3 must not change what a strategic merge patch produces.
func TestV3MatchesV2PatchResults(t *testing.T) {
	v2 := NewV2PatchMetaResolver(StaticResourcesGetter{
		Resources: openapitesting.NewFakeResources(filepath.Join("testdata", "swagger.1.30.2.json")),
	})

	results := map[string]string{}
	for name, resolver := range map[string]PatchMetaResolver{"v2": v2, "v3": newV3Resolver()} {
		out := testDeployment()
		_, _, err := ApplyPatches(testDeployment(), out, containerMergePatch, resolver)
		require.NoError(t, err)

		encoded, err := json.Marshal(out)
		require.NoError(t, err)
		results[name] = string(encoded)
	}

	require.JSONEq(t, results["v2"], results["v3"])
}

// TestV3PatchMetaResolverFetchesEachGroupVersionOnce guards the caching that
// makes per-GVK resolution affordable: without it every patch on every
// reconcile would re-fetch and re-parse a group-version document.
func TestV3PatchMetaResolverFetchesEachGroupVersionOnce(t *testing.T) {
	counter := &countingRoot{Root: openapi3.NewRoot(openapitest.NewEmbeddedFileClient())}
	resolver := NewV3PatchMetaResolver(counter)

	for range 3 {
		_, err := resolver.LookupPatchMeta(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"})
		require.NoError(t, err)
		_, err = resolver.LookupPatchMeta(schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"})
		require.NoError(t, err)
	}

	require.Equal(t, map[schema.GroupVersion]int{
		{Group: "apps", Version: "v1"}:  1,
		{Group: "batch", Version: "v1"}: 1,
	}, counter.calls)
}

// TestV3PatchMetaResolverCachesFailures ensures a group-version that cannot be
// fetched isn't retried on every reconcile.
func TestV3PatchMetaResolverCachesFailures(t *testing.T) {
	counter := &countingRoot{Root: openapi3.NewRoot(openapitest.NewEmbeddedFileClient())}
	resolver := NewV3PatchMetaResolver(counter)
	missing := schema.GroupVersionKind{Group: "nonexistent.example.com", Version: "v1", Kind: "Widget"}

	for range 3 {
		_, err := resolver.LookupPatchMeta(missing)
		require.Error(t, err)
	}

	require.Equal(t, 1, counter.calls[missing.GroupVersion()])
}

type countingRoot struct {
	openapi3.Root
	calls map[schema.GroupVersion]int
}

func (c *countingRoot) GVSpec(gv schema.GroupVersion) (*spec3.OpenAPI, error) {
	if c.calls == nil {
		c.calls = map[schema.GroupVersion]int{}
	}
	c.calls[gv]++
	return c.Root.GVSpec(gv)
}
