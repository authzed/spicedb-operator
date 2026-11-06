//go:build e2e

package e2e

import (
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EqualCondition checks that a condition is equal to expected, except for its
// time. The observed generation of the condition must be at least generation.
func EqualCondition(expected metav1.Condition, generation int64) types.GomegaMatcher {
	now := metav1.Now()
	expected.LastTransitionTime = now
	expected.ObservedGeneration = 0
	normalize := func(c *metav1.Condition) *metav1.Condition {
		normalized := *c
		normalized.LastTransitionTime = now
		normalized.ObservedGeneration = 0
		return &normalized
	}
	return gomega.And(
		gomega.Not(gomega.BeNil()),
		gomega.HaveField("ObservedGeneration", gomega.BeNumerically(">=", generation)),
		gomega.WithTransform(normalize, gomega.Equal(&expected)),
	)
}
