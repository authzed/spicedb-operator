package v1alpha1

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/authzed/spicedb-operator/pkg/util"
)

func NewPatch(nn types.NamespacedName, generation int64) *AuthzedEnterpriseCluster {
	return &AuthzedEnterpriseCluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       AuthzedEnterpriseClusterKind,
			APIVersion: SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{Namespace: nn.Namespace, Name: nn.Name, Generation: generation},
	}
}

func (c *AuthzedEnterpriseCluster) NamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Name:      c.Name,
		Namespace: c.Namespace,
	}
}

const ConditionTypePaused = "Paused"

func NewPausedCondition() metav1.Condition {
	return metav1.Condition{
		Type:               ConditionTypePaused,
		Status:             metav1.ConditionTrue,
		Reason:             "PausedByLabel",
		LastTransitionTime: metav1.NewTime(time.Now()),
		Message:            fmt.Sprintf("Controller pause requested via label: %s", util.PausedControllerSelectorKey),
	}
}

func NewSelfPausedCondition() metav1.Condition {
	return metav1.Condition{
		Type:               ConditionTypePaused,
		Status:             metav1.ConditionTrue,
		Reason:             "PausedByController",
		LastTransitionTime: metav1.NewTime(time.Now()),
		Message:            fmt.Sprintf("Reconiciliation has been paused by the controller; see other conditions for more information. When ready, unpause by removing the %s label", util.PausedControllerSelectorKey),
	}
}

func NewValidatingConfigCondition(secretHash string) metav1.Condition {
	return metav1.Condition{
		Type:               "Validating",
		Status:             metav1.ConditionTrue,
		Reason:             "ConfigChanged",
		LastTransitionTime: metav1.NewTime(time.Now()),
		Message:            fmt.Sprintf("Validating config with secret hash %q", secretHash),
	}
}

func NewInvalidConfigCondition(secretHash string, err error) metav1.Condition {
	return metav1.Condition{
		Type:               "ValidatingFailed",
		Status:             metav1.ConditionTrue,
		Reason:             "InvalidConfig",
		LastTransitionTime: metav1.NewTime(time.Now()),
		Message:            fmt.Sprintf("Error validating config with secret hash %q: %s", secretHash, err),
	}
}

func NewVerifyingMigrationCondition() metav1.Condition {
	return metav1.Condition{
		Type:               "Migrating",
		Status:             metav1.ConditionTrue,
		Reason:             "VerifyingMigrationLevel",
		LastTransitionTime: metav1.NewTime(time.Now()),
		Message:            "Checking if cluster's datastore is migrated",
	}
}

func NewMigratingCondition(engine, headRevision string) metav1.Condition {
	return metav1.Condition{
		Type:               "Migrating", // TODO: constants, etc
		Status:             metav1.ConditionTrue,
		Reason:             "MigratingDatastoreToHead",
		LastTransitionTime: metav1.NewTime(time.Now()),
		Message:            fmt.Sprintf("Migrating %s datastore to %s", engine, headRevision),
	}
}

func NewMigrationFailedCondition(engine, headRevision string, err error) metav1.Condition {
	return metav1.Condition{
		Type:               "Migrating", // TODO: constants, etc
		Status:             metav1.ConditionFalse,
		Reason:             "MigrationFailed",
		LastTransitionTime: metav1.NewTime(time.Now()),
		Message:            fmt.Sprintf("Migrating %s datastore to %s failed with error: %s", engine, headRevision, err),
	}
}
