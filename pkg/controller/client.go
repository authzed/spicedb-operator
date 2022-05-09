package controller

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

// ShouldRetry returns true if the error is transient.
// It returns a delay if the server suggested one.
func ShouldRetry(err error) (bool, time.Duration) {
	if seconds, shouldRetry := apierrors.SuggestsClientDelay(err); shouldRetry {
		return true, time.Duration(seconds) * time.Second
	}
	if utilnet.IsConnectionReset(err) ||
		apierrors.IsInternalError(err) ||
		apierrors.IsTimeout(err) ||
		apierrors.IsServerTimeout(err) ||
		apierrors.IsTooManyRequests(err) ||
		apierrors.IsUnexpectedServerError(err) {
		return true, 0
	}
	return false, 0
}

func (r *SpiceDBClusterHandler) PatchStatus(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error {
	for _, c := range patch.Status.Conditions {
		c.ObservedGeneration = patch.Generation
	}
	patch.Status.ObservedGeneration = patch.Generation
	data, err := client.Apply.Data(patch)
	if err != nil {
		return err
	}
	_, err = r.client.Resource(v1alpha1ClusterGVR).Namespace(patch.Namespace).Patch(ctx, patch.Name, types.ApplyPatchType, data, metadata.PatchForceOwned, "status")
	return err
}

func (r *SpiceDBClusterHandler) Patch(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error {
	data, err := client.Apply.Data(patch)
	if err != nil {
		return err
	}
	_, err = r.client.Resource(v1alpha1ClusterGVR).Namespace(patch.Namespace).Patch(ctx, patch.Name, types.ApplyPatchType, data, metadata.PatchForceOwned)
	return err
}
