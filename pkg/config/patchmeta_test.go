package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/openapi/openapitest"
	"k8s.io/client-go/openapi3"
	"k8s.io/kube-openapi/pkg/spec3"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

// testFixtureDir holds OpenAPI v3 documents captured from a Kubernetes 1.34
// apiserver, for exactly the group-versions the operator patches.
const testFixtureDir = "openapiv3.1.34"

// newTestPatchMetaResolver builds a resolver over the pinned v3 fixtures. It
// backs the whole patch corpus, so those tests exercise the same resolution
// path the operator uses in production.
func newTestPatchMetaResolver() *V3PatchMetaResolver {
	return NewV3PatchMetaResolver(openapi3.NewRoot(
		openapitest.NewFileClient(filepath.Join("testdata", testFixtureDir)),
	))
}

// patchedKinds are every kind the operator applies patches to, and so every
// kind that must resolve from the pinned fixtures.
var patchedKinds = []schema.GroupVersionKind{
	{Group: "", Version: "v1", Kind: "ServiceAccount"},
	{Group: "", Version: "v1", Kind: "Service"},
	{Group: "apps", Version: "v1", Kind: "Deployment"},
	{Group: "batch", Version: "v1", Kind: "Job"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "Role"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "RoleBinding"},
	{Group: "policy", Version: "v1", Kind: "PodDisruptionBudget"},
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
	resolver := newTestPatchMetaResolver()

	for _, gvk := range patchedKinds {
		t.Run(gvk.Kind, func(t *testing.T) {
			t.Parallel()
			meta, err := resolver.LookupPatchMeta(gvk)
			require.NoError(t, err)
			require.NotNil(t, meta)
		})
	}
}

// TestV3PatchMetaResolverUnknownKind covers a kind missing from a group-version
// that does exist, which is the index miss rather than a failed fetch.
func TestV3PatchMetaResolverUnknownKind(t *testing.T) {
	_, err := newTestPatchMetaResolver().LookupPatchMeta(schema.GroupVersionKind{
		Group: "apps", Version: "v1", Kind: "NotAThing",
	})
	require.ErrorContains(t, err, "no OpenAPI v3 schema describes")
}

func TestV3PatchMetaResolverMissingGroupVersion(t *testing.T) {
	_, err := newTestPatchMetaResolver().LookupPatchMeta(schema.GroupVersionKind{
		Group: "nonexistent.example.com", Version: "v1", Kind: "Widget",
	})
	require.Error(t, err)
}

// TestV3PatchMetaResolverHonorsMergeKeys is the behavioral test: the v3 schema
// must yield the container merge key, so that patching one container leaves the
// other in place.
func TestV3PatchMetaResolverHonorsMergeKeys(t *testing.T) {
	out := testDeployment()
	count, patched, err := ApplyPatches(testDeployment(), out, containerMergePatch, newTestPatchMetaResolver())
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.True(t, patched)

	encoded, err := json.Marshal(out)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "patched-image:v1")
	require.Contains(t, string(encoded), "sidecar:v0",
		"sidecar was dropped, so containers were replaced rather than merged by name")
}

// TestV3PatchMetaResolverFetchesEachGroupVersionOnce guards the caching that
// makes per-GVK resolution affordable: without it every patch on every
// reconcile would re-fetch and re-parse a group-version document.
func TestV3PatchMetaResolverFetchesEachGroupVersionOnce(t *testing.T) {
	counter := &countingRoot{Root: openapi3.NewRoot(openapitest.NewFileClient(filepath.Join("testdata", testFixtureDir)))}
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
	}, counter.callCounts())
}

// TestV3PatchMetaResolverCachesFailures ensures a group-version that cannot be
// fetched isn't retried on every reconcile.
func TestV3PatchMetaResolverCachesFailures(t *testing.T) {
	counter := &countingRoot{Root: openapi3.NewRoot(openapitest.NewFileClient(filepath.Join("testdata", testFixtureDir)))}
	resolver := NewV3PatchMetaResolver(counter)
	missing := schema.GroupVersionKind{Group: "nonexistent.example.com", Version: "v1", Kind: "Widget"}

	for range 3 {
		_, err := resolver.LookupPatchMeta(missing)
		require.Error(t, err)
	}

	require.Equal(t, 1, counter.callCounts()[missing.GroupVersion()])
}

type countingRoot struct {
	openapi3.Root

	// Guarded because each group-version loads under its own sync.Once, so the
	// Onces do not serialize GVSpec against each other.
	mu    sync.Mutex
	calls map[schema.GroupVersion]int
}

func (c *countingRoot) GVSpec(gv schema.GroupVersion) (*spec3.OpenAPI, error) {
	c.mu.Lock()
	if c.calls == nil {
		c.calls = map[schema.GroupVersion]int{}
	}
	c.calls[gv]++
	c.mu.Unlock()
	return c.Root.GVSpec(gv)
}

func (c *countingRoot) callCounts() map[schema.GroupVersion]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.calls)
}

// TestV3PatchMetaResolverConcurrentLookups exercises the resolver the way the
// operator does: the handler chain patches ServiceAccount, Role and Service
// inside a parallel() block, so lookups for different group-versions race.
//
// The cache deliberately does not hold a lock across the fetch -- only the map
// is guarded, and each group-version loads under its own sync.Once -- so this
// is the test that would catch a regression in that arrangement. Run under
// -race for it to mean anything.
func TestV3PatchMetaResolverConcurrentLookups(t *testing.T) {
	counter := &countingRoot{Root: openapi3.NewRoot(
		openapitest.NewFileClient(filepath.Join("testdata", testFixtureDir)),
	)}
	resolver := NewV3PatchMetaResolver(counter)

	const goroutines = 8
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*len(patchedKinds))

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, gvk := range patchedKinds {
				meta, err := resolver.LookupPatchMeta(gvk)
				if err != nil {
					errs <- fmt.Errorf("%s: %w", gvk, err)
					continue
				}
				if meta == nil {
					errs <- fmt.Errorf("%s: nil patch meta", gvk)
				}
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	// Each group-version must be fetched exactly once even under contention;
	// that is the point of the per-group-version sync.Once.
	counts := counter.callCounts()
	for gv, calls := range counts {
		require.Equal(t, 1, calls, "group-version %s fetched %d times", gv, calls)
	}
	require.Len(t, counts, 5, "expected one fetch per patched group-version")
}
