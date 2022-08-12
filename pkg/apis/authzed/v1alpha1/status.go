package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Puppets the meta conditions helpers onto SpiceDBCluster.
// This is mainly to enable interfaces that have requirements on conditions, see
// the pause handler for an example.

// GetStatusConditions returns all status conditions.
func (c *SpiceDBCluster) GetStatusConditions() []metav1.Condition {
	return c.Status.Conditions
}

// FindStatusCondition finds the conditionType in conditions.
func (c *SpiceDBCluster) FindStatusCondition(conditionType string) *metav1.Condition {
	return meta.FindStatusCondition(c.Status.Conditions, conditionType)
}

// SetStatusCondition sets the corresponding condition in conditions to newCondition.
// conditions must be non-nil.
//  1. if the condition of the specified type already exists (all fields of the existing condition are updated to
//     newCondition, LastTransitionTime is set to now if the new status differs from the old status)
//  2. if a condition of the specified type does not exist (LastTransitionTime is set to now() if unset, and newCondition is appended)
func (c *SpiceDBCluster) SetStatusCondition(condition metav1.Condition) {
	meta.SetStatusCondition(&c.Status.Conditions, condition)
}

// RemoveStatusCondition removes the corresponding conditionType from conditions.
// conditions must be non-nil.
func (c *SpiceDBCluster) RemoveStatusCondition(conditionType string) {
	meta.RemoveStatusCondition(&c.Status.Conditions, conditionType)
}

// IsStatusConditionTrue returns true when the conditionType is present and set to `metav1.ConditionTrue`
func (c *SpiceDBCluster) IsStatusConditionTrue(conditionType string) bool {
	return meta.IsStatusConditionTrue(c.Status.Conditions, conditionType)
}

// IsStatusConditionFalse returns true when the conditionType is present and set to `metav1.ConditionFalse`
func (c *SpiceDBCluster) IsStatusConditionFalse(conditionType string) bool {
	return meta.IsStatusConditionFalse(c.Status.Conditions, conditionType)
}

// IsStatusConditionPresentAndEqual returns true when conditionType is present and equal to status.
func (c *SpiceDBCluster) IsStatusConditionPresentAndEqual(conditionType string, status metav1.ConditionStatus) bool {
	return meta.IsStatusConditionPresentAndEqual(c.Status.Conditions, conditionType, status)
}

// IsStatusConditionChanged returns true if the passed in condition is different
// from the condition of the same type.
func (c *SpiceDBCluster) IsStatusConditionChanged(conditionType string, condition *metav1.Condition) bool {
	existing := c.FindStatusCondition(conditionType)
	if existing == nil && condition == nil {
		return false
	}
	// if only one is nil, the condition has changed
	if (existing == nil && condition != nil) || (existing != nil && condition == nil) {
		return true
	}
	// if not nil, changed if the message or reason is different
	return existing.Message != condition.Message || existing.Reason != condition.Reason
}
