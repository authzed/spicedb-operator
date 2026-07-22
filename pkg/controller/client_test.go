package controller

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

// A resourceVersion in an apply patch acts as an optimistic-lock
// precondition, so a patch that carries one is rejected whenever the object
// changed since it was read - e.g. by a status patch earlier in the same
// reconcile, as in self-pause.
func TestPatchOmitsResourceVersion(t *testing.T) {
	dclient := fake.NewSimpleDynamicClient(scheme.Scheme)
	var payload []byte
	dclient.PrependReactor("patch", "spicedbclusters", func(action k8stesting.Action) (bool, runtime.Object, error) {
		payload = action.(k8stesting.PatchAction).GetPatch()
		return true, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": v1alpha1.SchemeGroupVersion.String(),
			"kind":       v1alpha1.SpiceDBClusterKind,
		}}, nil
	})
	c := &Controller{client: dclient}

	cluster := &v1alpha1.SpiceDBCluster{ObjectMeta: metav1.ObjectMeta{
		Name:            "test",
		Namespace:       "test",
		ResourceVersion: "42",
	}}
	require.NoError(t, c.Patch(t.Context(), cluster))

	var patch struct {
		Metadata map[string]any `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(payload, &patch))
	require.NotContains(t, patch.Metadata, "resourceVersion")
}
