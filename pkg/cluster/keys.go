package cluster

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/authzed/spicedb-operator/pkg/util"
)

var ManagedDependentSelector = util.MustParseSelector("authzed.com/managed-by=operator")

func GVRMetaNamespaceKeyer(gvr schema.GroupVersionResource, key string) string {
	return fmt.Sprintf("%s.%s.%s::%s", gvr.Resource, gvr.Version, gvr.Group, key)
}

func GVRMetaNamespaceKeyFunc(gvr schema.GroupVersionResource, obj interface{}) (string, error) {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		return d.Key, nil
	}
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		return "", err
	}
	return GVRMetaNamespaceKeyer(gvr, key), nil
}

func SplitGVRMetaNamespaceKey(key string) (gvr *schema.GroupVersionResource, namespace, name string, err error) {
	parts := strings.SplitN(key, "::", 2)
	if len(parts) != 2 {
		err = fmt.Errorf("error parsing key: %s", key)
		return
	}
	gvr, _ = schema.ParseResourceArg(parts[0])
	if gvr == nil {
		err = fmt.Errorf("error parsing gvr from key: %s", parts[0])
		return
	}
	namespace, name, err = cache.SplitMetaNamespaceKey(parts[1])
	return
}

func GetClusterKeyFromLabel(in interface{}) ([]string, error) {
	obj := in.(runtime.Object)
	objMeta, err := meta.Accessor(obj)
	if err != nil {
		return nil, err
	}
	objLabels := objMeta.GetLabels()
	stackKey, ok := objLabels[OwnerLabelKey]
	if !ok {
		return nil, fmt.Errorf("synced %s %s/%s is managed by the operator but not associated with any cluster", obj.GetObjectKind(), objMeta.GetNamespace(), objMeta.GetName())
	}

	// stackKey must be namespace/name
	if len(strings.SplitN(stackKey, "/", 2)) != 2 {
		return nil, fmt.Errorf("invalid cluster key label on %s %s/%s: %s", obj.GetObjectKind(), objMeta.GetNamespace(), objMeta.GetName(), stackKey)
	}
	return []string{stackKey}, nil
}
