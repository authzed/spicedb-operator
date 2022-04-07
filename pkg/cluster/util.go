package cluster

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

func findJobCondition(job *batchv1.Job, conditionType batchv1.JobConditionType) *batchv1.JobCondition {
	if job == nil {
		return nil
	}
	conditions := job.Status.Conditions
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

func jobConditionHasStatus(job *batchv1.Job, conditionType batchv1.JobConditionType, status corev1.ConditionStatus) bool {
	c := findJobCondition(job, conditionType)
	if c == nil {
		return false
	}
	return c.Status == status
}
