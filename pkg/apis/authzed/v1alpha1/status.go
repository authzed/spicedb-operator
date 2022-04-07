package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (c *AuthzedEnterpriseCluster) NewStatusPatch() *AuthzedEnterpriseCluster {
	return &AuthzedEnterpriseCluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       AuthzedEnterpriseClusterKind,
			APIVersion: SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{Namespace: c.Namespace, Name: c.Name, Generation: c.Generation},
		Status: ClusterStatus{
			ObservedGeneration: c.Status.ObservedGeneration,
			SecretHash:         c.Status.SecretHash,
			Conditions:         c.Status.Conditions,
		},
	}
}

func (c *AuthzedEnterpriseCluster) FindStatusCondition(conditionType string) *metav1.Condition {
	return meta.FindStatusCondition(c.Status.Conditions, conditionType)
}

func (c *AuthzedEnterpriseCluster) SetStatusCondition(condition metav1.Condition) {
	meta.SetStatusCondition(&c.Status.Conditions, condition)
}
