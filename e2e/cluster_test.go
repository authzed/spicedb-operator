//go:build e2e

package e2e

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io/ioutil"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/restmapper"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/cluster"
)

var (
	v1alpha1ClusterGVR = v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.AuthzedEnterpriseClusterResourceName)

	//go:embed cockroach.yaml
	cockroachyaml []byte
)

var _ = Describe("AuthzedEnterpriseClusters", func() {
	var client dynamic.Interface
	var kclient kubernetes.Interface

	BeforeEach(func() {
		c, err := dynamic.NewForConfig(restConfig)
		Expect(err).To(Succeed())
		client = c

		k, err := kubernetes.NewForConfig(restConfig)
		Expect(err).To(Succeed())
		kclient = k
	})

	Describe("With invalid config", func() {
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
			Expect(err).To(Succeed())
			_, err = client.Resource(v1alpha1ClusterGVR).Namespace("test").Create(ctx, &unstructured.Unstructured{Object: u}, metav1.CreateOptions{})
			Expect(err).To(Succeed())
			DeferCleanup(client.Resource(v1alpha1ClusterGVR).Namespace("test").Delete, ctx, "test", metav1.DeleteOptions{})

			var c *v1alpha1.AuthzedEnterpriseCluster
			Eventually(func(g Gomega) {
				out, err := client.Resource(v1alpha1ClusterGVR).Namespace("test").Get(ctx, "test", metav1.GetOptions{})
				g.Expect(err).To(Succeed())
				g.Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(out.Object, &c)).To(Succeed())
				condition := meta.FindStatusCondition(c.Status.Conditions, "ValidatingFailed")
				g.Expect(condition).To(EqualCondition(v1alpha1.NewInvalidConfigCondition("", fmt.Errorf("datastore_engine is a required field"))))
			}).Should(Succeed())
		})
	})

	Describe("With CockroachDB", Ordered, Label("cockroachdb"), func() {
		BeforeAll(func() {
			dc, err := discovery.NewDiscoveryClientForConfig(restConfig)
			Expect(err).To(Succeed())
			mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))
			decoder := yaml.NewYAMLToJSONDecoder(ioutil.NopCloser(bytes.NewReader(cockroachyaml)))
			objs := make([]*unstructured.Unstructured, 0, 4)
			var crdb *appsv1.StatefulSet
			for {
				o := map[string]interface{}{}
				if err := decoder.Decode(&o); err != nil {
					break
				}
				u := unstructured.Unstructured{
					Object: o,
				}
				objs = append(objs, &u)
				gvk := u.GroupVersionKind()
				if gvk.Kind == "StatefulSet" {
					Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &crdb)).To(Succeed())
				}
				mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
				Expect(err).To(Succeed())
				_, err = client.Resource(mapping.Resource).Namespace("test").Create(context.Background(), &u, metav1.CreateOptions{})
				Expect(err).To(Succeed())
				DeferCleanup(client.Resource(mapping.Resource).Namespace("test").Delete, context.Background(), u.GetName(), metav1.DeleteOptions{})
			}
			Expect(len(objs)).To(Equal(5))

			By("waiting for crdb to start...")
			Eventually(func(g Gomega) {
				out, err := kclient.AppsV1().StatefulSets("test").Get(context.Background(), crdb.GetName(), metav1.GetOptions{})
				g.Expect(err).To(Succeed())
				g.Expect(out.Status.ReadyReplicas).ToNot(BeZero())
			}).Should(Succeed())
			By("crdb running.")
		})

		When("a valid AuthzedEnterpriseCluster is created for crdb", Ordered, func() {
			BeforeAll(func() {
				ctx, cancel := context.WithCancel(context.Background())
				DeferCleanup(cancel)

				secret := corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "spicedb",
						Namespace: "test",
					},
					StringData: map[string]string{
						"datastore_uri":     "postgresql://root:unused@cockroachdb-public:26257/defaultdb?sslmode=disable",
						"migration_secrets": "kaitain-bootstrap-token=testtesttesttest,sharewith-bootstrap-token=testtesttesttest,thumper-bootstrap-token=testtesttesttest,metrics-proxy-token=testtesttesttest",
						"preshared_key":     "testtesttesttest",
					},
				}
				_, err := kclient.CoreV1().Secrets("test").Create(ctx, &secret, metav1.CreateOptions{})
				Expect(err).To(Succeed())
				DeferCleanup(kclient.CoreV1().Secrets("test").Delete, ctx, "spicedb", metav1.DeleteOptions{})

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
						Config: map[string]string{
							"datastore_engine": "cockroachdb",
						},
						SecretRef: "spicedb",
					},
				}
				u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(aec)
				Expect(err).To(Succeed())
				_, err = client.Resource(v1alpha1ClusterGVR).Namespace("test").Create(ctx, &unstructured.Unstructured{Object: u}, metav1.CreateOptions{})
				Expect(err).To(Succeed())
				DeferCleanup(client.Resource(v1alpha1ClusterGVR).Namespace("test").Delete, ctx, "test", metav1.DeleteOptions{})

				var c *v1alpha1.AuthzedEnterpriseCluster
				Eventually(func(g Gomega) {
					out, err := client.Resource(v1alpha1ClusterGVR).Namespace("test").Get(ctx, "test", metav1.GetOptions{})
					g.Expect(err).To(Succeed())
					g.Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(out.Object, &c)).To(Succeed())

					fmt.Println(c.Status)
					condition := meta.FindStatusCondition(c.Status.Conditions, "Migrating")
					g.Expect(condition).To(EqualCondition(v1alpha1.NewMigratingCondition("cockroachdb", "head")))
				}).Should(Succeed())

				jobs, err := kclient.BatchV1().Jobs("test").List(ctx, metav1.ListOptions{
					LabelSelector: fmt.Sprintf("%s=%s", cluster.ComponentLabelKey, cluster.ComponentMigrationJobLabelValue),
				})
				Expect(err).To(Succeed())
				Expect(len(jobs.Items)).To(Equal(1))

				Eventually(func(g Gomega) {
					job := &jobs.Items[0]
					job, err := kclient.BatchV1().Jobs("test").Get(ctx, job.Name, metav1.GetOptions{})
					g.Expect(err).To(Succeed())
					g.Expect(job.Status.Succeeded).ToNot(BeZero())
				}).Should(Succeed())
			})

			Describe("with a migrated datastore", func() {
				It("creates a spicedb cluster", func() {
					ctx, cancel := context.WithCancel(context.Background())
					DeferCleanup(cancel)

					var deps *appsv1.DeploymentList
					Eventually(func(g Gomega) {
						var err error
						deps, err = kclient.AppsV1().Deployments("test").List(ctx, metav1.ListOptions{
							LabelSelector: fmt.Sprintf("%s=%s", cluster.ComponentLabelKey, cluster.ComponentSpiceDBLabelValue),
						})
						g.Expect(err).To(Succeed())
						g.Expect(len(deps.Items)).To(Equal(1))
						g.Expect(deps.Items[0].Status.AvailableReplicas).ToNot(BeZero())
						fmt.Println(deps.Items[0].Status)
					}).Should(Succeed())
				})

				When("the spicedb cluster is running", func() {
					It("deletes the migration job", func() {
						ctx, cancel := context.WithCancel(context.Background())
						DeferCleanup(cancel)

						Eventually(func(g Gomega) {
							jobs, err := kclient.BatchV1().Jobs("test").List(ctx, metav1.ListOptions{
								LabelSelector: fmt.Sprintf("%s=%s", cluster.ComponentLabelKey, cluster.ComponentMigrationJobLabelValue),
							})
							g.Expect(err).To(Succeed())
							g.Expect(len(jobs.Items)).To(BeZero())
						}).Should(Succeed())
					})
				})
			})
		})
	})
})
