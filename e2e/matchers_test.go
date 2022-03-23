package e2e

import (
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type GomegaMatcher interface {
	Match(actual interface{}) (success bool, err error)
	FailureMessage(actual interface{}) (message string)
	NegatedFailureMessage(actual interface{}) (message string)
}

// EqualCondition checks if conditions are equal except for their times
func EqualCondition(expected metav1.Condition) types.GomegaMatcher {
	now := metav1.Now()
	expected.LastTransitionTime = now
	var setNow = func(c *metav1.Condition) *metav1.Condition {
		c.LastTransitionTime = now
		return c
	}
	return gomega.And(gomega.Not(gomega.BeNil()), gomega.WithTransform(setNow, gomega.Equal(&expected)))
}
