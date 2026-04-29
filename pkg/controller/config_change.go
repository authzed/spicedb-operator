package controller

import (
	"cmp"
	"context"
	"slices"

	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/hash"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

type ConfigChangedHandler struct {
	patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	next        handler.ContextHandler
}

func (c *ConfigChangedHandler) Handle(ctx context.Context) {
	cluster := CtxCluster.MustValue(ctx)
	secrets := CtxSecrets.Value(ctx)
	var secretHash string

	// Build hash from only the specific credential keys that are referenced,
	// so changes to unrelated secret keys don't trigger spurious reconciles.
	type credKey struct{ secret, key string }
	var credKeys []credKey

	creds := cluster.Spec.Credentials
	if creds == nil && cluster.Spec.SecretRef != "" {
		// SecretRef path: always datastore_uri, migration_secrets, and preshared_key from the same secret.
		credKeys = []credKey{
			{cluster.Spec.SecretRef, "datastore_uri"},
			{cluster.Spec.SecretRef, "migration_secrets"},
			{cluster.Spec.SecretRef, "preshared_key"},
		}
	} else if creds != nil {
		if creds.DatastoreURI != nil && !creds.DatastoreURI.Skip {
			k := creds.DatastoreURI.Key
			if k == "" {
				k = "datastore_uri"
			}
			credKeys = append(credKeys, credKey{creds.DatastoreURI.SecretName, k})
			// When MigrationSecrets is not explicitly configured, it defaults to inheriting
			// the DatastoreURI secret. Hash the migration_secrets key from that secret so
			// changes to it trigger reconciliation.
			if creds.MigrationSecrets == nil {
				credKeys = append(credKeys, credKey{creds.DatastoreURI.SecretName, "migration_secrets"})
			}
		}
		if creds.PresharedKey != nil && !creds.PresharedKey.Skip {
			k := creds.PresharedKey.Key
			if k == "" {
				k = "preshared_key"
			}
			credKeys = append(credKeys, credKey{creds.PresharedKey.SecretName, k})
		}
		if creds.MigrationSecrets != nil && !creds.MigrationSecrets.Skip {
			k := creds.MigrationSecrets.Key
			if k == "" {
				k = "migration_secrets"
			}
			credKeys = append(credKeys, credKey{creds.MigrationSecrets.SecretName, k})
		}
	}

	// Sort for determinism.
	slices.SortFunc(credKeys, func(a, b credKey) int {
		if n := cmp.Compare(a.secret, b.secret); n != 0 {
			return n
		}
		return cmp.Compare(a.key, b.key)
	})

	type hashEntry struct {
		Secret string
		Key    string
		Value  string
	}
	hashData := make([]hashEntry, 0, len(credKeys))
	for _, ck := range credKeys {
		sec := secrets[ck.secret]
		if sec == nil {
			continue
		}
		hashData = append(hashData, hashEntry{Secret: ck.secret, Key: ck.key, Value: string(sec.Data[ck.key])})
	}
	if len(hashData) > 0 {
		secretHash = hash.SecureObject(hashData)
		ctx = CtxSecretHash.WithValue(ctx, secretHash)
	}
	status := &v1alpha1.SpiceDBCluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       v1alpha1.SpiceDBClusterKind,
			APIVersion: v1alpha1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{Namespace: cluster.Namespace, Name: cluster.Name, Generation: cluster.Generation},
		Status:     *cluster.Status.DeepCopy(),
	}

	preconditionsFailedCondition := cluster.FindStatusCondition(v1alpha1.ConditionTypePreconditionsFailed)
	if cluster.GetGeneration() != status.Status.ObservedGeneration || secretHash != status.Status.SecretHash ||
		(preconditionsFailedCondition != nil && preconditionsFailedCondition.Reason == v1alpha1.ConditionReasonMissingSecret) {
		logr.FromContextOrDiscard(ctx).V(4).Info("spicedb configuration changed")
		status.Status.ObservedGeneration = cluster.GetGeneration()
		status.Status.SecretHash = secretHash
		status.RemoveStatusCondition(v1alpha1.ConditionTypePreconditionsFailed)
		status.SetStatusCondition(v1alpha1.NewValidatingConfigCondition(secretHash))
		if err := c.patchStatus(ctx, status); err != nil {
			QueueOps.RequeueAPIErr(ctx, err)
			return
		}
	}
	cluster.Status = status.Status
	ctx = CtxCluster.WithValue(ctx, cluster)
	c.next.Handle(ctx)
}
