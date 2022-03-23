package util

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

const PausedControllerSelectorKey = "authzed.com/controller-paused"

func NamespacedName(c metav1.Object) types.NamespacedName {
	return types.NamespacedName{
		Name:      c.GetName(),
		Namespace: c.GetNamespace(),
	}
}

var NotPausedSelector = MustParseSelector("!" + PausedControllerSelectorKey)

func MustParseSelector(selector string) labels.Selector {
	s, err := labels.Parse(selector)
	if err != nil {
		panic(err)
	}
	return s
}

func IsPaused(object runtime.Object) bool {
	objMeta, err := meta.Accessor(object)
	if err != nil {
		return false
	}
	objLabels := objMeta.GetLabels()
	if objLabels == nil {
		return false
	}

	_, ok := objLabels[PausedControllerSelectorKey]
	return ok
}

func IsSelfPaused(object runtime.Object) bool {
	objMeta, err := meta.Accessor(object)
	if err != nil {
		return false
	}
	objLabels := objMeta.GetLabels()
	if objLabels == nil {
		return false
	}

	pauser, ok := objLabels[PausedControllerSelectorKey]
	if !ok {
		return false
	}
	return pauser == string(objMeta.GetUID())
}
