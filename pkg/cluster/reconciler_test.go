package cluster

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	kscheme "k8s.io/client-go/kubernetes/scheme"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

func TestReconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, kscheme.AddToScheme(scheme))
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	dclient := dynamicfake.NewSimpleDynamicClient(scheme)
	kclient := k8sfake.NewSimpleClientset()

	ctrl, err := NewController(context.Background(), dclient, kclient)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go ctrl.Start(ctx, 2)

	cluster := &v1alpha1.AuthzedEnterpriseCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test",
		},
		Spec: v1alpha1.ClusterSpec{
			Config:    map[string]string{},
			SecretRef: "",
		},
	}
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cluster)
	_, err = dclient.Resource(v1alpha1ClusterGVR).Namespace("test").Create(ctx, &unstructured.Unstructured{Object: u}, metav1.CreateOptions{})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		out, err := dclient.Resource(v1alpha1ClusterGVR).Namespace("test").Get(ctx, "test", metav1.GetOptions{})
		require.NoError(t, err)
		var c *v1alpha1.AuthzedEnterpriseCluster
		require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(out.Object, &c))
		fmt.Printf("%#v\n", out)
		return c.Status.ObservedGeneration != 0
	}, 10*time.Second, 100*time.Millisecond)
}
