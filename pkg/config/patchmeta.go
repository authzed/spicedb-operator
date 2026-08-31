package config

import (
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/openapi3"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"k8s.io/kubectl/pkg/util/openapi"
)

// groupVersionKindExtensionKey is the OpenAPI extension recording which
// GroupVersionKinds a schema describes.
const groupVersionKindExtensionKey = "x-kubernetes-group-version-kind"

// PatchMetaResolver supplies the strategic merge metadata for a kind: the merge
// keys and patch strategies that decide whether a list in a patch is merged
// with the existing list or replaces it wholesale.
//
// It exists so that the OpenAPI version backing that answer is an
// implementation detail, and so that resolution stays per-GVK and on demand.
// Both matter for memory: the OpenAPI v2 document describes the entire cluster
// and costs ~100MiB of retained heap to parse, whereas v3 is served per
// group-version and the operator only ever patches a handful of them.
type PatchMetaResolver interface {
	LookupPatchMeta(gvk schema.GroupVersionKind) (strategicpatch.LookupPatchMeta, error)
}

// V3PatchMetaResolver resolves patch metadata from the apiserver's OpenAPI v3
// endpoints, fetching and indexing one group-version at a time and caching the
// result for the lifetime of the resolver.
type V3PatchMetaResolver struct {
	root openapi3.Root

	// mu guards cached only. The fetch itself happens under each entry's own
	// sync.Once, so a slow group-version doesn't block lookups for others.
	mu     sync.Mutex
	cached map[schema.GroupVersion]*groupVersionSchemas
}

var _ PatchMetaResolver = (*V3PatchMetaResolver)(nil)

func NewV3PatchMetaResolver(root openapi3.Root) *V3PatchMetaResolver {
	return &V3PatchMetaResolver{
		root:   root,
		cached: make(map[schema.GroupVersion]*groupVersionSchemas),
	}
}

func (r *V3PatchMetaResolver) LookupPatchMeta(gvk schema.GroupVersionKind) (strategicpatch.LookupPatchMeta, error) {
	gvs, err := r.groupVersion(gvk.GroupVersion())
	if err != nil {
		return nil, err
	}
	s, ok := gvs.byGVK[gvk]
	if !ok {
		return nil, fmt.Errorf("no OpenAPI v3 schema describes %s", gvk)
	}
	return strategicpatch.PatchMetaFromOpenAPIV3{Schema: s, SchemaList: gvs.all}, nil
}

func (r *V3PatchMetaResolver) groupVersion(gv schema.GroupVersion) (*groupVersionSchemas, error) {
	r.mu.Lock()
	gvs, ok := r.cached[gv]
	if !ok {
		gvs = &groupVersionSchemas{}
		r.cached[gv] = gvs
	}
	r.mu.Unlock()

	gvs.once.Do(func() { gvs.load(r.root, gv) })
	return gvs, gvs.err
}

// groupVersionSchemas holds one group-version's schemas, indexed for lookup.
type groupVersionSchemas struct {
	once sync.Once

	// byGVK indexes schemas by the kinds they describe. kubectl's apply path
	// rescans every schema in the group-version on each patch; indexing once
	// keeps repeated reconciles cheap.
	byGVK map[schema.GroupVersionKind]*spec.Schema

	// all is every schema in the group-version. It is required, not merely
	// convenient: PatchMetaFromOpenAPIV3 resolves $refs against it as it walks
	// into nested objects.
	all map[string]*spec.Schema

	err error
}

func (g *groupVersionSchemas) load(root openapi3.Root, gv schema.GroupVersion) {
	gvSpec, err := root.GVSpec(gv)
	if err != nil {
		g.err = fmt.Errorf("couldn't fetch OpenAPI v3 schema for %s: %w", gv, err)
		return
	}
	if gvSpec == nil || gvSpec.Components == nil {
		g.err = fmt.Errorf("OpenAPI v3 schema for %s has no components", gv)
		return
	}

	g.all = gvSpec.Components.Schemas
	g.byGVK = make(map[schema.GroupVersionKind]*spec.Schema, len(gvSpec.Components.Schemas))
	for _, s := range gvSpec.Components.Schemas {
		for _, gvk := range describedGVKs(s.Extensions) {
			g.byGVK[gvk] = s
		}
	}
}

// describedGVKs reads the GroupVersionKinds a v3 schema describes from its
// extensions. The extension is usually a list but is permitted to be a single
// object, so both shapes are accepted, matching kubectl's apply path.
func describedGVKs(ext spec.Extensions) []schema.GroupVersionKind {
	var list []map[string]string
	if err := ext.GetObject(groupVersionKindExtensionKey, &list); err == nil {
		return gvksFromMaps(list)
	}

	var single map[string]string
	if err := ext.GetObject(groupVersionKindExtensionKey, &single); err == nil {
		return gvksFromMaps([]map[string]string{single})
	}

	return nil
}

func gvksFromMaps(maps []map[string]string) []schema.GroupVersionKind {
	gvks := make([]schema.GroupVersionKind, 0, len(maps))
	for _, m := range maps {
		if m["kind"] == "" {
			continue
		}
		gvks = append(gvks, schema.GroupVersionKind{
			Group:   m["group"],
			Version: m["version"],
			Kind:    m["kind"],
		})
	}
	return gvks
}

// V2PatchMetaResolver resolves patch metadata from a whole-cluster OpenAPI v2
// schema.
//
// This is the pre-v3 behavior, kept because the patch test fixtures are pinned
// v2 swagger documents and because it is the fallback for apiservers that don't
// serve OpenAPI v3 (before Kubernetes 1.24). Prefer V3PatchMetaResolver: this
// one cannot avoid parsing a description of every resource in the cluster.
type V2PatchMetaResolver struct {
	getter openapi.OpenAPIResourcesGetter
}

var _ PatchMetaResolver = (*V2PatchMetaResolver)(nil)

func NewV2PatchMetaResolver(getter openapi.OpenAPIResourcesGetter) *V2PatchMetaResolver {
	return &V2PatchMetaResolver{getter: getter}
}

func (r *V2PatchMetaResolver) LookupPatchMeta(gvk schema.GroupVersionKind) (strategicpatch.LookupPatchMeta, error) {
	resources, err := r.getter.OpenAPISchema()
	if err != nil {
		return nil, fmt.Errorf("couldn't load OpenAPI v2 schema: %w", err)
	}
	// A nil schema is passed through rather than treated as an error: it means
	// the document doesn't describe this kind, and strategic merge then falls
	// back to replacing lists instead of merging them. That is long-standing
	// behavior here and is preserved deliberately.
	return strategicpatch.NewPatchMetaFromOpenAPI(resources.LookupResource(gvk)), nil
}

// StaticResourcesGetter adapts an already-resolved openapi.Resources to the
// lazy openapi.OpenAPIResourcesGetter interface, for callers that have the
// schema in hand and don't need it loaded on demand.
type StaticResourcesGetter struct {
	Resources openapi.Resources
}

func (s StaticResourcesGetter) OpenAPISchema() (openapi.Resources, error) {
	return s.Resources, nil
}
