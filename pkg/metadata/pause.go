package metadata

import (
	"k8s.io/apimachinery/pkg/labels"
)

const PausedControllerSelectorKey = "authzed.com/controller-paused"

var (
	NotPausedSelector     = MustParseSelector("!" + PausedControllerSelectorKey)
	NotPausedRequirements = RequirementsFromSelector(NotPausedSelector)
)

func MustParseSelector(selector string) labels.Selector {
	s, err := labels.Parse(selector)
	if err != nil {
		panic(err)
	}
	return s
}

func RequirementsFromSelector(selector labels.Selector) labels.Requirements {
	r, _ := selector.Requirements()
	return r
}
