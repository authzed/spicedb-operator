//go:build e2e

package e2e

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io/ioutil"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/util/cert"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/cluster"
)

var (
	v1alpha1ClusterGVR = v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.SpiceDBClusterResourceName)

	//go:embed cockroach.yaml
	cockroachyaml []byte
)

var _ = Describe("SpiceDBClusters", func() {
	var (
		client  dynamic.Interface
		kclient kubernetes.Interface
	)

	AssertMigrationJobCleanup := func(args func() (string, string)) {
		namespace, owner := args()
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)

		Eventually(func(g Gomega) {
			jobs, err := kclient.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", cluster.ComponentLabelKey, cluster.ComponentMigrationJobLabelValue, cluster.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(jobs.Items)).To(BeZero())
		}).Should(Succeed())
	}

	AssertHealthySpiceDBCluster := func(image string, args func() (string, string), logMatcher types.GomegaMatcher) {
		namespace, owner := args()
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)

		var deps *appsv1.DeploymentList
		Eventually(func(g Gomega) {
			var err error
			deps, err = kclient.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", cluster.ComponentLabelKey, cluster.ComponentSpiceDBLabelValue, cluster.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(deps.Items)).To(Equal(1))
			g.Expect(deps.Items[0].Spec.Template.Spec.Containers[0].Image).To(Equal(image))
			GinkgoWriter.Println(deps.Items[0].Status)
			g.Expect(deps.Items[0].Status.AvailableReplicas).ToNot(BeZero())

			endpoint, err := kclient.CoreV1().Endpoints(namespace).Get(ctx, owner, metav1.GetOptions{})
			g.Expect(err).To(Succeed())
			g.Expect(endpoint).ToNot(BeNil())
		}).Should(Succeed())

		By("not having startup warnings")
		var buf bytes.Buffer
		Tail(&deps.Items[0], func(g Gomega) {
			g.Eventually(buf.Len()).ShouldNot(BeZero())
			g.Consistently(buf.String()).Should(logMatcher)
		}, GinkgoWriter, &buf)
	}

	AssertDependentResourceCleanup := func(namespace, owner, secretName string) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// the secret should remain despite deleting the cluster
		Consistently(func(g Gomega) {
			secret, err := kclient.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
			g.Expect(err).To(Succeed())
			g.Expect(secret).ToNot(BeNil())
		}).Should(Succeed())

		// dependent resources should all be cleaned up
		Eventually(func(g Gomega) {
			list, err := kclient.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", cluster.ComponentLabelKey, cluster.ComponentSpiceDBLabelValue, cluster.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(list.Items)).To(BeZero())
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			list, err := kclient.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", cluster.ComponentLabelKey, cluster.ComponentServiceLabel, cluster.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(list.Items)).To(BeZero())
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			list, err := kclient.CoreV1().ServiceAccounts(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", cluster.ComponentLabelKey, cluster.ComponentServiceAccountLabel, cluster.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(list.Items)).To(BeZero())
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			list, err := kclient.RbacV1().Roles(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", cluster.ComponentLabelKey, cluster.ComponentRoleLabel, cluster.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(list.Items)).To(BeZero())
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			list, err := kclient.RbacV1().RoleBindings(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", cluster.ComponentLabelKey, cluster.ComponentRoleBindingLabel, cluster.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(list.Items)).To(BeZero())
		}).Should(Succeed())
	}

	AssertMigrationsCompleted := func(image string, args func() (string, string)) {
		namespace, name := args()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		Eventually(func(g Gomega) {
			var c *v1alpha1.SpiceDBCluster
			out, err := client.Resource(v1alpha1ClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
			g.Expect(err).To(Succeed())
			g.Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(out.Object, &c)).To(Succeed())

			GinkgoWriter.Println(c.Status)
			condition := meta.FindStatusCondition(c.Status.Conditions, "Migrating")
			g.Expect(condition).To(EqualCondition(v1alpha1.NewMigratingCondition("cockroachdb", "head")))
		}).Should(Succeed())

		Eventually(func(g Gomega) {
			jobs, err := kclient.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s", cluster.ComponentLabelKey, cluster.ComponentMigrationJobLabelValue),
			})
			Expect(err).To(Succeed())

			Expect(len(jobs.Items)).ToNot(BeZero())

			job := &jobs.Items[len(jobs.Items)-1]
			Expect(job.Spec.Template.Spec.Containers[0].Image).To(Equal(image))
			TailF(job)
			Eventually(func(g Gomega) {
				job, err := kclient.BatchV1().Jobs(namespace).Get(ctx, job.Name, metav1.GetOptions{})
				g.Expect(err).To(Succeed())
				g.Expect(job.Status.Succeeded).ToNot(BeZero())
			}).Should(Succeed())
		})
	}

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
			aec := &v1alpha1.SpiceDBCluster{
				TypeMeta: metav1.TypeMeta{
					Kind:       v1alpha1.SpiceDBClusterKind,
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

			var c *v1alpha1.SpiceDBCluster
			Eventually(func(g Gomega) {
				out, err := client.Resource(v1alpha1ClusterGVR).Namespace("test").Get(ctx, "test", metav1.GetOptions{})
				g.Expect(err).To(Succeed())
				g.Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(out.Object, &c)).To(Succeed())
				condition := meta.FindStatusCondition(c.Status.Conditions, "ValidatingFailed")
				g.Expect(condition).To(EqualCondition(v1alpha1.NewInvalidConfigCondition("", fmt.Errorf("[datastoreEngine is a required field, secret must be provided]"))))
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

		When("a valid SpiceDBCluster is created (no TLS)", Ordered, func() {
			var cluster *v1alpha1.SpiceDBCluster

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

				cluster = &v1alpha1.SpiceDBCluster{
					TypeMeta: metav1.TypeMeta{
						Kind:       v1alpha1.SpiceDBClusterKind,
						APIVersion: v1alpha1.SchemeGroupVersion.String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test",
						Namespace: "test",
					},
					Spec: v1alpha1.ClusterSpec{
						Config: map[string]string{
							"datastoreEngine": "cockroachdb",
							"envPrefix":       "SPICEDB_ENTERPRISE",
							"cmd":             "spicedb-enterprise",
						},
						SecretRef: "spicedb",
					},
				}
				u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cluster)
				Expect(err).To(Succeed())
				_, err = client.Resource(v1alpha1ClusterGVR).Namespace(cluster.Namespace).Create(ctx, &unstructured.Unstructured{Object: u}, metav1.CreateOptions{})
				Expect(err).To(Succeed())

				AssertMigrationsCompleted("authzed-spicedb-enterprise:dev", func() (string, string) { return cluster.Namespace, cluster.Name })
			})

			AfterAll(func() {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				Expect(client.Resource(v1alpha1ClusterGVR).Namespace(cluster.Namespace).Delete(ctx, cluster.Name, metav1.DeleteOptions{})).To(Succeed())

				AssertDependentResourceCleanup(cluster.Namespace, cluster.Name, "spicedb")

				Expect(kclient.CoreV1().Secrets(cluster.Namespace).Delete(ctx, "spicedb", metav1.DeleteOptions{})).To(Succeed())
			})

			Describe("with a migrated datastore", func() {
				It("creates a spicedb cluster", func() {
					AssertHealthySpiceDBCluster("authzed-spicedb-enterprise:dev",
						func() (string, string) { return cluster.Namespace, cluster.Name },
						Not(ContainSubstring("ERROR: kuberesolver")))
				})
				When("the spicedb cluster is running", func() {
					It("cleans up the migration job", func() {
						AssertMigrationJobCleanup(func() (string, string) {
							return cluster.Namespace, cluster.Name
						})
					})
				})
			})
		})

		When("a valid SpiceDBCluster is created (with TLS)", Ordered, func() {
			var spiceCluster *v1alpha1.SpiceDBCluster

			BeforeAll(func() {
				ctx, cancel := context.WithCancel(context.Background())
				DeferCleanup(cancel)

				spiceCluster = &v1alpha1.SpiceDBCluster{
					TypeMeta: metav1.TypeMeta{
						Kind:       v1alpha1.SpiceDBClusterKind,
						APIVersion: v1alpha1.SchemeGroupVersion.String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test2",
						Namespace: "test",
					},
					Spec: v1alpha1.ClusterSpec{
						Config: map[string]string{
							"datastoreEngine": "cockroachdb",
							"envPrefix":       "SPICEDB_ENTERPRISE",
							"cmd":             "spicedb-enterprise",
							"tlsSecretName":   "spicedb-grpc-tls",
						},
						SecretRef: "spicedb2",
					},
				}

				certPem, keyPem, err := cert.GenerateSelfSignedCertKey("test2", nil, []string{
					"localhost",
					"test2." + spiceCluster.Namespace,
					fmt.Sprintf("test2.%s.svc.spiceCluster.local", spiceCluster.Namespace),
				})
				Expect(err).To(Succeed())

				tlsSecret := corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "spicedb-grpc-tls",
						Namespace: spiceCluster.Namespace,
					},
					Data: map[string][]byte{
						"tls.key": keyPem,
						"tls.crt": certPem,
					},
				}
				_, err = kclient.CoreV1().Secrets(spiceCluster.Namespace).Create(ctx, &tlsSecret, metav1.CreateOptions{})
				Expect(err).To(Succeed())

				secret := corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "spicedb2",
						Namespace: "test",
					},
					StringData: map[string]string{
						"datastore_uri":     "postgresql://root:unused@cockroachdb-public:26257/defaultdb?sslmode=disable",
						"migration_secrets": "kaitain-bootstrap-token=testtesttesttest,sharewith-bootstrap-token=testtesttesttest,thumper-bootstrap-token=testtesttesttest,metrics-proxy-token=testtesttesttest",
						"preshared_key":     "testtesttesttest",
					},
				}
				_, err = kclient.CoreV1().Secrets(spiceCluster.Namespace).Create(ctx, &secret, metav1.CreateOptions{})
				Expect(err).To(Succeed())

				u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(spiceCluster)
				Expect(err).To(Succeed())
				_, err = client.Resource(v1alpha1ClusterGVR).Namespace(spiceCluster.Namespace).Create(ctx, &unstructured.Unstructured{Object: u}, metav1.CreateOptions{})
				Expect(err).To(Succeed())

				AssertMigrationsCompleted("authzed-spicedb-enterprise:dev",
					func() (string, string) {
						return spiceCluster.Namespace, spiceCluster.Name
					})
			})

			AfterAll(func() {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				Expect(client.Resource(v1alpha1ClusterGVR).Namespace(spiceCluster.Namespace).Delete(ctx, spiceCluster.Name, metav1.DeleteOptions{})).To(Succeed())

				AssertDependentResourceCleanup(spiceCluster.Namespace, spiceCluster.Name, "spicedb2")

				Expect(kclient.CoreV1().Secrets(spiceCluster.Namespace).Delete(ctx, "spicedb2", metav1.DeleteOptions{})).To(Succeed())
			})

			Describe("with a migrated datastore", func() {
				It("creates a spicedb cluster", func() {
					AssertHealthySpiceDBCluster("authzed-spicedb-enterprise:dev",
						func() (string, string) {
							return spiceCluster.Namespace, spiceCluster.Name
						}, Not(ContainSubstring("ERROR: kuberesolver")))
				})

				When("the spicedb cluster is running", func() {
					It("cleans up the migration job", func() {
						AssertMigrationJobCleanup(func() (string, string) {
							return spiceCluster.Namespace, spiceCluster.Name
						})
					})

					When("the image name/tag are updated", Ordered, func() {
						var imageName string

						BeforeAll(func() {
							newConfig := cluster.OperatorConfig{
								ImageTag:  "updated",
								ImageName: "authzed-spicedb-enterprise",
							}
							imageName = strings.Join([]string{newConfig.ImageName, newConfig.ImageTag}, ":")
							WriteConfig(newConfig)
						})

						It("migrates to the latest version", func() {
							AssertMigrationsCompleted(imageName,
								func() (string, string) {
									return spiceCluster.Namespace, spiceCluster.Name
								})
						})

						It("updates the deployment", func() {
							AssertHealthySpiceDBCluster(imageName, func() (string, string) {
								return spiceCluster.Namespace, spiceCluster.Name
							}, Not(ContainSubstring("ERROR: kuberesolver")))
						})

						It("deletes the migration job", func() {
							AssertMigrationJobCleanup(func() (string, string) {
								return spiceCluster.Namespace, spiceCluster.Name
							})
						})
					})
				})
			})
		})
	})
})
