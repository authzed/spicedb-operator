package config

import (
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/kube-openapi/pkg/util/proto"
	"k8s.io/kubectl/pkg/util/openapi"
)

// groupVersionKindExtensionKey is the OpenAPI extension that maps a model
// definition to the GroupVersionKinds it describes.
const groupVersionKindExtensionKey = "x-kubernetes-group-version-kind"

// StaticResourcesGetter adapts an already-resolved openapi.Resources to the
// lazy openapi.OpenAPIResourcesGetter interface, for callers that have the
// schema in hand and don't need it loaded on demand.
type StaticResourcesGetter struct {
	Resources openapi.Resources
}

func (s StaticResourcesGetter) OpenAPISchema() (openapi.Resources, error) {
	return s.Resources, nil
}

// ModelsOnlyResourcesGetter resolves the cluster's OpenAPI v2 schema on first
// use and retains only the parts strategic merge patching needs.
//
// kubectl's own openapi.Resources implementation additionally retains the raw
// gnostic *openapi_v2.Document for the lifetime of the process, solely so that
// GetConsumes can walk its paths. That document is the majority of the schema's
// memory cost -- on the order of 60MiB of protobuf struct graph for a cluster
// with a couple hundred resources -- and this operator never calls GetConsumes.
// Scoping the document to the parse lets the garbage collector reclaim it.
type ModelsOnlyResourcesGetter struct {
	client discovery.OpenAPISchemaInterface

	once      sync.Once
	resources openapi.Resources
	err       error
}

var _ openapi.OpenAPIResourcesGetter = (*ModelsOnlyResourcesGetter)(nil)

func NewModelsOnlyResourcesGetter(client discovery.OpenAPISchemaInterface) *ModelsOnlyResourcesGetter {
	return &ModelsOnlyResourcesGetter{client: client}
}

// OpenAPISchema fetches and parses the schema on first call and memoizes the
// result, including errors: a failure is not retried, matching the behavior of
// kubectl's CachedOpenAPIParser.
func (g *ModelsOnlyResourcesGetter) OpenAPISchema() (openapi.Resources, error) {
	g.once.Do(func() {
		// doc is scoped to this closure deliberately; nothing may retain it
		// past the parse or the memory saving is lost.
		doc, err := g.client.OpenAPISchema()
		if err != nil {
			g.err = fmt.Errorf("couldn't fetch OpenAPI schema: %w", err)
			return
		}
		models, err := proto.NewOpenAPIData(doc)
		if err != nil {
			g.err = fmt.Errorf("couldn't parse OpenAPI schema: %w", err)
			return
		}
		g.resources = newModelsOnlyResources(models)
	})
	return g.resources, g.err
}

// modelsOnlyResources implements openapi.Resources over parsed models alone.
type modelsOnlyResources struct {
	kindToModel map[schema.GroupVersionKind]string
	models      proto.Models
}

var _ openapi.Resources = (*modelsOnlyResources)(nil)

func newModelsOnlyResources(models proto.Models) *modelsOnlyResources {
	kindToModel := make(map[schema.GroupVersionKind]string)
	for _, name := range models.ListModels() {
		model := models.LookupModel(name)
		if model == nil {
			continue
		}
		for _, gvk := range parseGroupVersionKind(model) {
			if len(gvk.Kind) > 0 {
				kindToModel[gvk] = name
			}
		}
	}
	return &modelsOnlyResources{kindToModel: kindToModel, models: models}
}

func (r *modelsOnlyResources) LookupResource(gvk schema.GroupVersionKind) proto.Schema {
	name, ok := r.kindToModel[gvk]
	if !ok {
		return nil
	}
	return r.models.LookupModel(name)
}

// GetConsumes is part of openapi.Resources but cannot be answered without the
// raw OpenAPI document, which is deliberately not retained. Nothing in this
// operator calls it; strategic merge patching only needs LookupResource.
func (r *modelsOnlyResources) GetConsumes(_ schema.GroupVersionKind, _ string) []string {
	return nil
}

// parseGroupVersionKind reads the GroupVersionKinds a model describes from its
// extensions. It mirrors the unexported equivalent in k8s.io/kubectl's openapi
// package, including the map[interface{}]interface{} shape that kube-openapi's
// vendor extension decoding produces, so that lookups resolve identically.
func parseGroupVersionKind(s proto.Schema) []schema.GroupVersionKind {
	gvkExtension, ok := s.GetExtensions()[groupVersionKindExtensionKey]
	if !ok {
		return nil
	}

	gvkList, ok := gvkExtension.([]interface{})
	if !ok {
		return nil
	}

	result := make([]schema.GroupVersionKind, 0, len(gvkList))
	for _, item := range gvkList {
		gvkMap, ok := item.(map[interface{}]interface{})
		if !ok {
			continue
		}
		group, ok := gvkMap["group"].(string)
		if !ok {
			continue
		}
		version, ok := gvkMap["version"].(string)
		if !ok {
			continue
		}
		kind, ok := gvkMap["kind"].(string)
		if !ok {
			continue
		}
		result = append(result, schema.GroupVersionKind{Group: group, Version: version, Kind: kind})
	}
	return result
}
