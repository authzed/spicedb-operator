package metadata

import (
	"k8s.io/apimachinery/pkg/labels"
)

const PausedControllerSelectorKey = "authzed.com/controller-paused"

var NotPausedSelector = MustParseSelector("!" + PausedControllerSelectorKey)

func MustParseSelector(selector string) labels.Selector {
	s, err := labels.Parse(selector)
	if err != nil {
		panic(err)
	}
	return s
}
