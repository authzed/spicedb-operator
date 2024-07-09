package config

import (
	"bytes"
	"encoding/json"
	"fmt"

	jsonpatch "github.com/evanphx/json-patch"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	applymetav1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/kubectl/pkg/util/openapi"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

const wildcard = "*"

// ApplyPatches applies a set of patches to an object.
// It returns the number of patches applied, a bool indicating whether there
// were matching patches and the input differed from the output, and any errors
// that occurred.
func ApplyPatches[K any](object, out K, patches []v1alpha1.Patch, resources openapi.Resources) (int, bool, error) {
	// marshal object to json for patching
	encoded, err := json.Marshal(object)
	if err != nil {
		return 0, false, fmt.Errorf("error marshalling object to patch: %w", err)
	}

	initial := encoded

	// HACK: Unmarshal into TypeMeta to determine `kind` of incoming object.
	// The ApplyConfiguration objects don't have any getters, so there's no
	// common interface to use to get their `kind`, even though they all have
	// a kind field. Golang also doesn't support writing generic functions over
	// struct members. This hack can be removed if we add getters to the
	// generated applyconfigurations or golang supports this via generics:
	// - https://github.com/golang/go/issues/51259
	// - https://github.com/golang/go/issues/48522
	// - https://github.com/kubernetes/kubernetes/issues/113773
	var typeMeta applymetav1.TypeMetaApplyConfiguration
	if err := json.Unmarshal(encoded, &typeMeta); err != nil {
		return 0, false, fmt.Errorf("unable to extract type info from object for patching: %w", err)
	}

	if typeMeta.Kind == nil {
		return 0, false, fmt.Errorf("object doesn't specify kind: %v", object)
	}

	count := 0
	errs := make([]error, 0)
	for i, p := range patches {
		if p.Kind == *typeMeta.Kind || p.Kind == wildcard {
			// determine if the patch is a strategic merge or a json6902 patch
			decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(p.Patch), 100)
			var json6902op jsonpatch.Operation
			err = decoder.Decode(&json6902op)
			if err != nil {
				errs = append(errs, fmt.Errorf("error decoding patch %d: %w", i, err))
				continue
			}

			// if there's no operation, it's a strategic merge patch, not 6902
			if json6902op.Kind() == "unknown" {
				jsonPatch, err := utilyaml.ToJSON(p.Patch)
				if err != nil {
					errs = append(errs, fmt.Errorf("error converting patch %d to json: %w", i, err))
					continue
				}
				gv, err := schema.ParseGroupVersion(*typeMeta.APIVersion)
				if err != nil {
					errs = append(errs, fmt.Errorf("error applying patch %d, to object: %w", i, err))
					continue
				}
				gvkSchema := resources.LookupResource(gv.WithKind(*typeMeta.Kind))
				patched, err := strategicpatch.StrategicMergePatchUsingLookupPatchMeta(encoded, jsonPatch, strategicpatch.NewPatchMetaFromOpenAPI(gvkSchema))
				if err != nil {
					errs = append(errs, fmt.Errorf("error applying patch %d, to object: %w", i, err))
					continue
				}
				encoded = patched
			} else {
				json6902patch := jsonpatch.Patch([]jsonpatch.Operation{json6902op})
				patched, err := json6902patch.Apply(encoded)
				if err != nil {
					errs = append(errs, fmt.Errorf("error applying patch %d to object: %w", i, err))
					continue
				}
				encoded = patched
			}
			count++
		}
	}

	if err := json.Unmarshal(encoded, out); err != nil {
		errs = append(errs, fmt.Errorf("error converting back to object: %w", err))
	}

	diff := false
	if count > 0 {
		// return true if there were patches defined and the output bytes differ
		diff = !bytes.Equal(encoded, initial)
	}
	return count, diff, kerrors.NewAggregate(errs)
}
