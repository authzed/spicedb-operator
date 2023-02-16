package config

import (
	"bytes"
	"encoding/json"
	"fmt"

	jsonpatch "github.com/evanphx/json-patch"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

const wildcard = "*"

// ApplyPatches applies a set of patches to an object.
// It returns the number of patches applied, a bool indicating whether there
// were matching patches and the input differed from the output, and any errors
// that occurred.
// K = the kube Kind object, i.e. Deployment
// A = pointer to the kube ApplyConfiguration object, i.e. *DeploymentApplyConfiguration
func ApplyPatches[K, A any](applyConfig A, object K, patches []v1alpha1.Patch) (int, bool, error) {
	// marshal applyConfig to json for patching
	encoded, err := json.Marshal(applyConfig)
	if err != nil {
		return 0, false, fmt.Errorf("error marshalling applyConfig to patch: %w", err)
	}

	initial := encoded

	if err := json.Unmarshal(encoded, object); err != nil {
		return 0, false, fmt.Errorf("unable to convert object to %T for patching: %w", object, err)
	}

	kind := any(object).(runtime.Object).GetObjectKind().GroupVersionKind().Kind
	if kind == "" {
		return 0, false, fmt.Errorf("applyConfig doesn't specify kind: %#v", applyConfig)
	}

	count := 0
	errs := make([]error, 0)
	for i, p := range patches {
		if p.Kind == kind || p.Kind == wildcard {
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
				patched, err := strategicpatch.StrategicMergePatch(encoded, jsonPatch, object)
				if err != nil {
					errs = append(errs, fmt.Errorf("error applying patch %d, to applyConfig: %w", i, err))
					continue
				}
				if err := json.Unmarshal(patched, &applyConfig); err != nil {
					errs = append(errs, fmt.Errorf("error converting patched applyConfig for patch %d back to applyConfig: %w", i, err))
					continue
				}
				encoded = patched
			} else {
				json6902patch := jsonpatch.Patch([]jsonpatch.Operation{json6902op})
				patched, err := json6902patch.Apply(encoded)
				if err != nil {
					errs = append(errs, fmt.Errorf("error applying patch %d to applyConfig: %w", i, err))
					continue
				}
				if err := json.Unmarshal(patched, &applyConfig); err != nil {
					errs = append(errs, fmt.Errorf("error converting patched applyConfig from patch %d back to applyConfig: %w", i, err))
					continue
				}
				encoded = patched
			}
			count++
		}
	}

	if err := json.Unmarshal(encoded, applyConfig); err != nil {
		errs = append(errs, fmt.Errorf("error converting fully patched applyConfig back to applyConfig: %w", err))
	}

	return count, count > 0 && !bytes.Equal(encoded, initial), errors.NewAggregate(errs)
}
