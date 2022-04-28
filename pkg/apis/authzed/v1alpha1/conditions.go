package v1alpha1

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func (c *SpiceDBCluster) NamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Name:      c.Name,
		Namespace: c.Namespace,
	}
}

const (
	ConditionTypeValidating   = "Validating"
	ConditionValidatingFailed = "ValidatingFailed"
)

func NewValidatingConfigCondition(secretHash string) metav1.Condition {
	return metav1.Condition{
		Type:               ConditionTypeValidating,
		Status:             metav1.ConditionTrue,
		Reason:             "ConfigChanged",
		LastTransitionTime: metav1.NewTime(time.Now()),
		Message:            fmt.Sprintf("Validating config with secret hash %q", secretHash),
	}
}

func NewInvalidConfigCondition(secretHash string, err error) metav1.Condition {
	return metav1.Condition{
		Type:               ConditionValidatingFailed,
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

const ConditionTypeMigrating = "Migrating"

func NewMigratingCondition(engine, headRevision string) metav1.Condition {
	return metav1.Condition{
		Type:               ConditionTypeMigrating,
		Status:             metav1.ConditionTrue,
		Reason:             "MigratingDatastoreToHead",
		LastTransitionTime: metav1.NewTime(time.Now()),
		Message:            fmt.Sprintf("Migrating %s datastore to %s", engine, headRevision),
	}
}

func NewMigrationFailedCondition(engine, headRevision string, err error) metav1.Condition {
	return metav1.Condition{
		Type:               ConditionTypeMigrating, // TODO: constants, etc
		Status:             metav1.ConditionFalse,
		Reason:             "MigrationFailed",
		LastTransitionTime: metav1.NewTime(time.Now()),
		Message:            fmt.Sprintf("Migrating %s datastore to %s failed with error: %s", engine, headRevision, err),
	}
}
