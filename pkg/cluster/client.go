package cluster

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

var force = true

func (r *SpiceDBClusterHandler) PatchStatus(ctx context.Context, patch *v1alpha1.AuthzedEnterpriseCluster) error {
	for _, c := range patch.Status.Conditions {
		c.ObservedGeneration = patch.Generation
	}
	patch.Status.ObservedGeneration = patch.Generation
	data, err := client.Apply.Data(patch)
	if err != nil {
		return err
	}
	// TODO: are some errors requeuable and some not?
	_, err = r.client.Resource(v1alpha1ClusterGVR).Namespace(patch.Namespace).Patch(ctx, patch.Name, types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: "authzed-operator", Force: &force}, "status")
	return err
}

func (r *SpiceDBClusterHandler) Patch(ctx context.Context, patch *v1alpha1.AuthzedEnterpriseCluster) error {
	data, err := client.Apply.Data(patch)
	if err != nil {
		return err
	}
	// TODO: are some errors requeuable and some not?
	_, err = r.client.Resource(v1alpha1ClusterGVR).Namespace(patch.Namespace).Patch(ctx, patch.Name, types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: "authzed-operator", Force: &force})
	return err
}
