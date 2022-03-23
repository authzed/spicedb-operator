package e2e

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

var (
	v1alpha1ClusterGVR = v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.AuthzedEnterpriseClusterResourceName)
)

var _ = Describe("AuthzedEnterpriseClusters", func() {
	var client dynamic.Interface

	BeforeEach(func() {
		c, err := dynamic.NewForConfig(restConfig)
		Expect(err).To(Succeed())
		client = c
	})

	It("Reports invalid config on the status", func() {
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)
		aec := &v1alpha1.AuthzedEnterpriseCluster{
			TypeMeta: metav1.TypeMeta{
				Kind:       v1alpha1.AuthzedEnterpriseClusterKind,
				APIVersion: v1alpha1.SchemeGroupVersion.String(),
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: "test",
			},
			Spec: v1alpha1.ClusterSpec{
				Config:    map[string]string{},
				SecretRef: "",
			},
		}
		u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(aec)
		_, err = client.Resource(v1alpha1ClusterGVR).Namespace("test").Create(ctx, &unstructured.Unstructured{Object: u}, metav1.CreateOptions{})
		Expect(err).To(Succeed())

		var c *v1alpha1.AuthzedEnterpriseCluster
		Eventually(func(g Gomega) {
			out, err := client.Resource(v1alpha1ClusterGVR).Namespace("test").Get(ctx, "test", metav1.GetOptions{})
			g.Expect(err).To(Succeed())
			g.Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(out.Object, &c)).To(Succeed())
			condition := meta.FindStatusCondition(c.Status.Conditions, "ValidatingFailed")
			g.Expect(condition).To(EqualCondition(v1alpha1.NewInvalidConfigCondition("", fmt.Errorf("Config.datastore_engine is a required field"))))
		}).Should(Succeed())
	})
})
