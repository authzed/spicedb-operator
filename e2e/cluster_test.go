//go:build e2e

package e2e

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"time"

	database "cloud.google.com/go/spanner/admin/database/apiv1"
	instances "cloud.google.com/go/spanner/admin/instance/apiv1"
	"github.com/authzed/controller-idioms/typed"
	"github.com/jzelinskie/stringz"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	adminpb "google.golang.org/genproto/googleapis/spanner/admin/database/v1"
	"google.golang.org/genproto/googleapis/spanner/admin/instance/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ktypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/util/cert"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/config"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

var (
	v1alpha1ClusterGVR = v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.SpiceDBClusterResourceName)

	spicedbEnvPrefix = stringz.DefaultEmpty(os.Getenv("SPICEDB_ENV_PREFIX"), "SPICEDB")
	spicedbCmd       = stringz.DefaultEmpty(os.Getenv("SPICEDB_CMD"), "spicedb")

	noop        = func(namespace string) error { return nil }
	clusterNoop = func(kclient kubernetes.Interface, namespace string) error { return nil }

	//go:embed cockroach.yaml
	cockroachyaml []byte

	//go:embed postgresql.yaml
	postgresqlyaml []byte

	//go:embed mysql.yaml
	mysqlyaml []byte

	//go:embed spanner.yaml
	spanneryaml []byte
)

type datastoreDef struct {
	description       string
	label             string
	definition        []byte
	definedObjs       int
	datastoreUri      string
	datastoreEngine   string
	datastoreSetup    func(namespace string) error
	clusterSetup      func(kclient kubernetes.Interface, namespace string) error
	passthroughConfig map[string]string
}

var datastoreDefs = []datastoreDef{
	{
		description:     "With Spanner",
		label:           "spanner",
		definition:      spanneryaml,
		definedObjs:     2,
		datastoreUri:    "projects/fake-project-id/instances/fake-instance/databases/fake-database-id",
		datastoreEngine: "spanner",
		datastoreSetup: func(namespace string) error {
			ctx, cancel := context.WithCancel(context.Background())
			PortForward(namespace, "spanner-0", []string{"9010"}, ctx.Done())
			defer cancel()

			os.Setenv("SPANNER_EMULATOR_HOST", "localhost:9010")

			var instancesClient *instances.InstanceAdminClient
			Eventually(func() *instances.InstanceAdminClient {
				// Create instance
				client, err := instances.NewInstanceAdminClient(ctx)
				if err != nil {
					return nil
				}
				instancesClient = client
				return client
			}).Should(Not(BeNil()))

			defer func() { instancesClient.Close() }()

			var createInstanceOp *instances.CreateInstanceOperation
			Eventually(func(g Gomega) {
				var err error
				createInstanceOp, err = instancesClient.CreateInstance(ctx, &instance.CreateInstanceRequest{
					Parent:     "projects/fake-project-id",
					InstanceId: "fake-instance",
					Instance: &instance.Instance{
						Config:      "emulator-config",
						DisplayName: "Test Instance",
						NodeCount:   1,
					},
				})
				g.Expect(err).To(Succeed())
			}).Should(Succeed())

			spannerInstance, err := createInstanceOp.Wait(ctx)
			Expect(err).To(Succeed())

			// Create db
			adminClient, err := database.NewDatabaseAdminClient(ctx)
			Expect(err).To(Succeed())
			defer adminClient.Close()

			dbID := "fake-database-id"
			op, err := adminClient.CreateDatabase(ctx, &adminpb.CreateDatabaseRequest{
				Parent:          spannerInstance.Name,
				CreateStatement: "CREATE DATABASE `" + dbID + "`",
			})
			Expect(err).To(Succeed())

			_, err = op.Wait(ctx)
			Expect(err).To(Succeed())
			return err
		},
		clusterSetup: func(kclient kubernetes.Interface, namespace string) error {
			secret := corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "spanner-credentials",
					Namespace: namespace,
				},
				StringData: map[string]string{
					"credentials.json": "{}",
				},
			}
			_, err := kclient.CoreV1().Secrets(namespace).Create(context.Background(), &secret, metav1.CreateOptions{})
			Expect(err).To(Succeed())
			return err
		},
		passthroughConfig: map[string]string{
			"datastoreSpannerEmulatorHost": "spanner-service:9010",
			"spannerCredentials":           "spanner-credentials",
		},
	},
	{
		description:     "With CockroachDB",
		label:           "cockroachdb",
		definition:      cockroachyaml,
		definedObjs:     5,
		datastoreUri:    "postgresql://root:unused@cockroachdb-public:26257/defaultdb?sslmode=disable",
		datastoreEngine: "cockroachdb",
		datastoreSetup:  noop,
		clusterSetup:    clusterNoop,
	},
	{
		description:     "With Postgresql",
		label:           "postgresql",
		definition:      postgresqlyaml,
		definedObjs:     2,
		datastoreUri:    "postgresql://postgres:testpassword@postgresql-db-public:5432/postgres?sslmode=disable",
		datastoreEngine: "postgres",
		datastoreSetup:  noop,
		clusterSetup:    clusterNoop,
	},
	{
		description:     "With MySQL",
		label:           "mysql",
		definition:      mysqlyaml,
		definedObjs:     2,
		datastoreUri:    "root:password@tcp(mysql-public:3306)/mysql?parseTime=true",
		datastoreEngine: "mysql",
		datastoreSetup:  noop,
		clusterSetup:    clusterNoop,
	},
}

func (def datastoreDef) namespace() string {
	return fmt.Sprintf("test-%s", def.label)
}

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
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", metadata.ComponentLabelKey, metadata.ComponentMigrationJobLabelValue, metadata.OwnerLabelKey, owner),
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
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", metadata.ComponentLabelKey, metadata.ComponentSpiceDBLabelValue, metadata.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(deps.Items)).To(Equal(1))
			g.Expect(deps.Items[0].Spec.Template.Spec.Containers[0].Image).To(Equal(image))
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
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", metadata.ComponentLabelKey, metadata.ComponentSpiceDBLabelValue, metadata.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(list.Items)).To(BeZero())
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			list, err := kclient.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", metadata.ComponentLabelKey, metadata.ComponentServiceLabel, metadata.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(list.Items)).To(BeZero())
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			list, err := kclient.CoreV1().ServiceAccounts(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", metadata.ComponentLabelKey, metadata.ComponentServiceAccountLabel, metadata.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(list.Items)).To(BeZero())
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			list, err := kclient.RbacV1().Roles(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", metadata.ComponentLabelKey, metadata.ComponentRoleLabel, metadata.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(list.Items)).To(BeZero())
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			list, err := kclient.RbacV1().RoleBindings(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", metadata.ComponentLabelKey, metadata.ComponentRoleBindingLabel, metadata.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(list.Items)).To(BeZero())
		}).Should(Succeed())
	}

	AssertMigrationsCompleted := func(image, migration, phase string, args func() (string, string, string)) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		if migration == "" {
			migration = "head"
		}
		namespace, name, datastoreEngine := args()

		var condition *metav1.Condition
		Watch(ctx, client, v1alpha1ClusterGVR, ktypes.NamespacedName{Name: name, Namespace: namespace}, "0", func(c *v1alpha1.SpiceDBCluster) bool {
			condition = c.FindStatusCondition("Migrating")
			return condition == nil
		})
		Expect(condition).To(EqualCondition(v1alpha1.NewMigratingCondition(datastoreEngine, "head")))

		Eventually(func(g Gomega) {
			jobs, err := kclient.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s", metadata.ComponentLabelKey, metadata.ComponentMigrationJobLabelValue),
			})
			Expect(err).To(Succeed())

			Expect(len(jobs.Items)).ToNot(BeZero())

			job := &jobs.Items[len(jobs.Items)-1]
			Expect(job.Spec.Template.Spec.Containers[0].Image).To(Equal(image))
			Expect(job.Spec.Template.Spec.Containers[0].Command).To(ContainSubstring(migration))
			if phase != "" {
				foundPhase := false
				for _, e := range job.Spec.Template.Spec.Containers[0].Env {
					GinkgoWriter.Println(e)
					if e.Value == phase {
						foundPhase = true
					}
				}
				Expect(foundPhase).To(BeTrue())
			}

			if datastoreEngine == "spanner" {
				var spannerVolume corev1.Volume
				for _, v := range job.Spec.Template.Spec.Volumes {
					if v.Name == "spanner" {
						spannerVolume = v
						continue
					}
				}
				Expect(spannerVolume).NotTo(BeNil())

				var volMount corev1.VolumeMount
				for _, mount := range job.Spec.Template.Spec.Containers[0].VolumeMounts {
					if mount.Name == "spanner" {
						volMount = mount
					}
				}
				Expect(volMount).NotTo(BeNil())
			}

			TailF(job)
			Eventually(func(g Gomega) {
				job, err := kclient.BatchV1().Jobs(namespace).Get(ctx, job.Name, metav1.GetOptions{})
				GinkgoWriter.Println(job, err)
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
		testNamespace := "test"

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
					Namespace: testNamespace,
				},
				Spec: v1alpha1.ClusterSpec{
					Config:    []byte("{}"),
					SecretRef: "",
				},
			}
			u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(aec)
			Expect(err).To(Succeed())
			_, err = client.Resource(v1alpha1ClusterGVR).Namespace(testNamespace).Create(ctx, &unstructured.Unstructured{Object: u}, metav1.CreateOptions{})
			Expect(err).To(Succeed())
			DeferCleanup(client.Resource(v1alpha1ClusterGVR).Namespace(testNamespace).Delete, ctx, "test", metav1.DeleteOptions{})

			var c *v1alpha1.SpiceDBCluster
			watchCtx, watchCancel := context.WithTimeout(ctx, 3*time.Minute)
			watcher, err := client.Resource(v1alpha1ClusterGVR).Namespace(testNamespace).Watch(watchCtx, metav1.ListOptions{
				Watch:           true,
				ResourceVersion: "0",
			})
			Expect(err).To(Succeed())
			foundValidationFailed := false
			for event := range watcher.ResultChan() {
				Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(event.Object.(*unstructured.Unstructured).Object, &c)).To(Succeed())
				if c.Name != "test" {
					continue
				}
				GinkgoWriter.Println(c)
				condition := c.FindStatusCondition("ValidatingFailed")
				if condition != nil {
					foundValidationFailed = true
					Expect(condition).To(EqualCondition(v1alpha1.NewInvalidConfigCondition("", fmt.Errorf("[datastoreEngine is a required field, secret must be provided]"))))
					break
				}
			}
			watchCancel()
			Expect(foundValidationFailed).To(BeTrue())
		})
	})

	Describe("With missing secret", func() {
		testNamespace := "test"
		rv := "0"

		BeforeEach(func() {
			ctx, cancel := context.WithCancel(context.Background())
			DeferCleanup(cancel)

			_ = kclient.CoreV1().Secrets(testNamespace).Delete(ctx, "nonexistent", metav1.DeleteOptions{})

			aec := &v1alpha1.SpiceDBCluster{
				TypeMeta: metav1.TypeMeta{
					Kind:       v1alpha1.SpiceDBClusterKind,
					APIVersion: v1alpha1.SchemeGroupVersion.String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: testNamespace,
				},
				Spec: v1alpha1.ClusterSpec{
					Config:    []byte(`{"datastoreEngine": "memory"}`),
					SecretRef: "nonexistent",
				},
			}
			u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(aec)
			Expect(err).To(Succeed())
			_, err = client.Resource(v1alpha1ClusterGVR).Namespace(testNamespace).Create(ctx, &unstructured.Unstructured{Object: u}, metav1.CreateOptions{})
			Expect(err).To(Succeed())
			DeferCleanup(client.Resource(v1alpha1ClusterGVR).Namespace(testNamespace).Delete, ctx, "test", metav1.DeleteOptions{})
		})

		It("Reports missing secret on the status", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			DeferCleanup(cancel)

			var c *v1alpha1.SpiceDBCluster
			watcher, err := client.Resource(v1alpha1ClusterGVR).Namespace(testNamespace).Watch(ctx, metav1.ListOptions{
				Watch:           true,
				ResourceVersion: rv,
			})
			Expect(err).To(Succeed())
			foundMissingSecret := false
			for event := range watcher.ResultChan() {
				Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(event.Object.(*unstructured.Unstructured).Object, &c)).To(Succeed())
				if c.Name != "test" {
					continue
				}
				GinkgoWriter.Println(c)
				condition := c.FindStatusCondition(v1alpha1.ConditionTypePreconditionsFailed)
				if condition != nil {
					foundMissingSecret = true
					Expect(condition).To(EqualCondition(v1alpha1.NewMissingSecretCondition(ktypes.NamespacedName{
						Namespace: c.Namespace,
						Name:      "nonexistent",
					})))
					break
				}
			}
			Expect(foundMissingSecret).To(BeTrue())
		})

		Describe("when the secret is created", func() {
			BeforeEach(func() {
				ctx, cancel := context.WithCancel(context.Background())
				DeferCleanup(cancel)
				secret := corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "nonexistent",
						Namespace: testNamespace,
					},
					StringData: map[string]string{
						"logLevel":      "debug",
						"preshared_key": "testtesttesttest",
					},
				}
				_, err := kclient.CoreV1().Secrets(testNamespace).Create(ctx, &secret, metav1.CreateOptions{})
				Expect(err).To(Succeed())
			})

			It("removes the missing secret condition", func() {
				var c *v1alpha1.SpiceDBCluster
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				DeferCleanup(cancel)
				watcher, err := client.Resource(v1alpha1ClusterGVR).Namespace(testNamespace).Watch(ctx, metav1.ListOptions{
					Watch:           true,
					ResourceVersion: rv,
				})
				Expect(err).To(Succeed())

				foundMissingSecret := true
				for event := range watcher.ResultChan() {
					Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(event.Object.(*unstructured.Unstructured).Object, &c)).To(Succeed())
					if c.Name != "test" {
						continue
					}
					GinkgoWriter.Println(c)
					condition := c.FindStatusCondition(v1alpha1.ConditionTypePreconditionsFailed)
					if condition == nil {
						foundMissingSecret = false
						break
					}
				}
				Expect(foundMissingSecret).To(BeFalse())
			})
		})
	})

	for _, dsDef := range datastoreDefs {
		dsDef := dsDef
		testNamespace := dsDef.namespace()
		Describe(dsDef.description, Ordered, Label(dsDef.label), func() {
			BeforeAll(func() {
				CreateNamespace(testNamespace)
				DeferCleanup(DeleteNamespace, testNamespace)

				dc, err := discovery.NewDiscoveryClientForConfig(restConfig)
				Expect(err).To(Succeed())
				mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))
				decoder := yaml.NewYAMLToJSONDecoder(ioutil.NopCloser(bytes.NewReader(dsDef.definition)))
				objs := make([]*unstructured.Unstructured, 0, 5)
				var db *appsv1.StatefulSet
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
						Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &db)).To(Succeed())
					}
					mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
					Expect(err).To(Succeed())
					_, err = client.Resource(mapping.Resource).Namespace(testNamespace).Create(context.Background(), &u, metav1.CreateOptions{})
					Expect(err).To(Succeed())
					DeferCleanup(client.Resource(mapping.Resource).Namespace(testNamespace).Delete, context.Background(), u.GetName(), metav1.DeleteOptions{})
				}
				Expect(len(objs)).To(Equal(dsDef.definedObjs))

				By(fmt.Sprintf("waiting for %s to start...", dsDef.label))
				Eventually(func(g Gomega) {
					out, err := kclient.AppsV1().StatefulSets(testNamespace).Get(context.Background(), db.GetName(), metav1.GetOptions{})
					g.Expect(err).To(Succeed())
					g.Expect(out.Status.ReadyReplicas).ToNot(BeZero())
				}).Should(Succeed())
				Eventually(func(g Gomega) {
					out, err := kclient.CoreV1().Pods(testNamespace).Get(context.Background(), db.GetName()+"-0", metav1.GetOptions{})
					g.Expect(err).To(Succeed())
					g.Expect(out.Status.Phase).To(Equal(corev1.PodRunning))
				}).Should(Succeed())
				By(fmt.Sprintf("%s running.", dsDef.label))

				Eventually(func(g Gomega) {
					err = dsDef.datastoreSetup(testNamespace)
					Expect(err).To(Succeed())
				}).Should(Succeed())
				By(fmt.Sprintf("%s setup complete.", dsDef.label))

				Eventually(func(g Gomega) {
					err = dsDef.clusterSetup(kclient, testNamespace)
					Expect(err).To(Succeed())
				}).Should(Succeed())
				By(fmt.Sprintf("%s cluster setup complete.", dsDef.label))
			})

			When("a valid SpiceDBCluster is created (no TLS)", Ordered, func() {
				var cluster *v1alpha1.SpiceDBCluster

				BeforeAll(func() {
					ctx, cancel := context.WithCancel(context.Background())
					DeferCleanup(cancel)

					secret := corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "spicedb",
							Namespace: testNamespace,
						},
						StringData: map[string]string{
							"logLevel":          "debug",
							"datastore_uri":     dsDef.datastoreUri,
							"preshared_key":     "testtesttesttest",
							"migration_secrets": "kaitain-bootstrap-token=testtesttesttest,sharewith-bootstrap-token=testtesttesttest,thumper-bootstrap-token=testtesttesttest,metrics-proxy-token=testtesttesttest",
						},
					}
					_, err := kclient.CoreV1().Secrets(testNamespace).Create(ctx, &secret, metav1.CreateOptions{})
					Expect(err).To(Succeed())

					config := map[string]any{
						"datastoreEngine": dsDef.datastoreEngine,
						"envPrefix":       spicedbEnvPrefix,
						"cmd":             spicedbCmd,
					}
					for k, v := range dsDef.passthroughConfig {
						config[k] = v
					}
					jsonConfig, err := json.Marshal(config)
					Expect(err).To(Succeed())
					cluster = &v1alpha1.SpiceDBCluster{
						TypeMeta: metav1.TypeMeta{
							Kind:       v1alpha1.SpiceDBClusterKind,
							APIVersion: v1alpha1.SchemeGroupVersion.String(),
						},
						ObjectMeta: metav1.ObjectMeta{
							Name:      fmt.Sprintf("test-%s", dsDef.label),
							Namespace: testNamespace,
						},
						Spec: v1alpha1.ClusterSpec{
							Config:    jsonConfig,
							SecretRef: "spicedb",
						},
					}
					u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cluster)
					Expect(err).To(Succeed())
					_, err = client.Resource(v1alpha1ClusterGVR).Namespace(cluster.Namespace).Create(ctx, &unstructured.Unstructured{Object: u}, metav1.CreateOptions{})
					Expect(err).To(Succeed())

					AssertMigrationsCompleted("spicedb:dev", "", "", func() (string, string, string) { return cluster.Namespace, cluster.Name, dsDef.datastoreEngine })
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
						AssertHealthySpiceDBCluster("spicedb:dev",
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

			When("a valid SpiceDBCluster and skipped migrations", Ordered, func() {
				var spiceCluster *v1alpha1.SpiceDBCluster

				It("Starts SpiceDB without migrating", func() {
					ctx, cancel := context.WithCancel(context.Background())
					DeferCleanup(cancel)

					secret := corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "spicedb-nomigrate",
							Namespace: testNamespace,
						},
						StringData: map[string]string{
							"logLevel":          "debug",
							"datastore_uri":     dsDef.datastoreUri,
							"preshared_key":     "testtesttesttest",
							"migration_secrets": "kaitain-bootstrap-token=testtesttesttest,sharewith-bootstrap-token=testtesttesttest,thumper-bootstrap-token=testtesttesttest,metrics-proxy-token=testtesttesttest",
						},
					}
					_, err := kclient.CoreV1().Secrets(testNamespace).Create(ctx, &secret, metav1.CreateOptions{})
					Expect(err).To(Succeed())
					DeferCleanup(kclient.CoreV1().Secrets(testNamespace).Delete, ctx, secret.Name, metav1.DeleteOptions{})
					config, err := json.Marshal(map[string]any{
						"skipMigrations":  true,
						"datastoreEngine": dsDef.datastoreEngine,
						"envPrefix":       spicedbEnvPrefix,
						"cmd":             spicedbCmd,
					})
					Expect(err).To(Succeed())
					spiceCluster = &v1alpha1.SpiceDBCluster{
						TypeMeta: metav1.TypeMeta{
							Kind:       v1alpha1.SpiceDBClusterKind,
							APIVersion: v1alpha1.SchemeGroupVersion.String(),
						},
						ObjectMeta: metav1.ObjectMeta{
							Name:      fmt.Sprintf("test-nomigrate-%s", dsDef.label),
							Namespace: testNamespace,
						},
						Spec: v1alpha1.ClusterSpec{
							Config:    config,
							SecretRef: "spicedb-nomigrate",
						},
					}
					u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(spiceCluster)
					Expect(err).To(Succeed())
					_, err = client.Resource(v1alpha1ClusterGVR).Namespace(testNamespace).Create(ctx, &unstructured.Unstructured{Object: u}, metav1.CreateOptions{})
					Expect(err).To(Succeed())
					DeferCleanup(client.Resource(v1alpha1ClusterGVR).Namespace(testNamespace).Delete, ctx, spiceCluster.Name, metav1.DeleteOptions{})

					var deps *appsv1.DeploymentList
					Eventually(func(g Gomega) {
						var err error
						deps, err = kclient.AppsV1().Deployments(spiceCluster.Namespace).List(ctx, metav1.ListOptions{
							LabelSelector: fmt.Sprintf("%s=%s,%s=%s", metadata.ComponentLabelKey, metadata.ComponentSpiceDBLabelValue, metadata.OwnerLabelKey, spiceCluster.Name),
						})
						g.Expect(err).To(Succeed())
						g.Expect(len(deps.Items)).To(Equal(1))
						GinkgoWriter.Println(deps.Items[0].Name)
						fmt.Println(deps)
					}).Should(Succeed())
				})
			})

			When("a valid SpiceDBCluster is created (with TLS)", Ordered, func() {
				var spiceCluster *v1alpha1.SpiceDBCluster

				BeforeAll(func() {
					ctx, cancel := context.WithCancel(context.Background())
					DeferCleanup(cancel)

					config := map[string]any{
						"datastoreEngine": dsDef.datastoreEngine,
						"envPrefix":       spicedbEnvPrefix,
						"cmd":             spicedbCmd,
						"tlsSecretName":   "spicedb-grpc-tls",
					}
					for k, v := range dsDef.passthroughConfig {
						config[k] = v
					}
					jsonConfig, err := json.Marshal(config)
					Expect(err).To(BeNil())
					spiceCluster = &v1alpha1.SpiceDBCluster{
						TypeMeta: metav1.TypeMeta{
							Kind:       v1alpha1.SpiceDBClusterKind,
							APIVersion: v1alpha1.SchemeGroupVersion.String(),
						},
						ObjectMeta: metav1.ObjectMeta{
							Name:      fmt.Sprintf("test2-%s", dsDef.label),
							Namespace: testNamespace,
						},
						Spec: v1alpha1.ClusterSpec{
							Config:    jsonConfig,
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
					DeferCleanup(kclient.CoreV1().Secrets(spiceCluster.Namespace).Delete, ctx, tlsSecret.Name, metav1.DeleteOptions{})

					secret := corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "spicedb2",
							Namespace: spiceCluster.Namespace,
						},
						StringData: map[string]string{
							"datastore_uri":     dsDef.datastoreUri,
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

					AssertMigrationsCompleted("spicedb:dev", "", "",
						func() (string, string, string) {
							return spiceCluster.Namespace, spiceCluster.Name, dsDef.datastoreEngine
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
						AssertHealthySpiceDBCluster("spicedb:dev",
							func() (string, string) {
								return testNamespace, spiceCluster.Name
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
								newConfig := config.OperatorConfig{
									ImageTag:  "updated",
									ImageName: "spicedb",
								}
								imageName = strings.Join([]string{newConfig.ImageName, newConfig.ImageTag}, ":")
								WriteConfig(newConfig)
							})

							AfterAll(func() {
								newConfig := config.OperatorConfig{
									ImageName: "spicedb",
									ImageTag:  "dev",
								}
								imageName = strings.Join([]string{newConfig.ImageName, newConfig.ImageTag}, ":")
								WriteConfig(newConfig)
							})

							It("migrates to the latest version", func() {
								AssertMigrationsCompleted(imageName, "", "",
									func() (string, string, string) {
										return spiceCluster.Namespace, spiceCluster.Name, dsDef.datastoreEngine
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

			When("a valid SpiceDBCluster with required upgrade edges", Ordered, func() {
				var spiceCluster *v1alpha1.SpiceDBCluster
				var migration string

				BeforeAll(func() {
					ctx, cancel := context.WithCancel(context.Background())
					DeferCleanup(cancel)

					switch dsDef.datastoreEngine {
					case "spanner":
						migration = "add-metadata-and-counters"
					case "cockroachdb":
						migration = "add-metadata-and-counters"
					case "postgres":
						migration = "add-ns-config-id"
					case "mysql":
						migration = "add_ns_config_id"
					}

					dev := config.SpiceDBState{Tag: "dev"}
					updated := config.SpiceDBState{Tag: "updated", Migration: migration, Phase: "phase2"}
					newConfig := config.OperatorConfig{
						RequiredEdges: map[string]string{
							dev.String(): updated.String(),
						},
						Nodes: map[string]config.SpiceDBState{
							dev.String():     dev,
							updated.String(): updated,
						},
					}
					WriteConfig(newConfig)

					config := map[string]any{
						"logLevel":        "debug",
						"datastoreEngine": dsDef.datastoreEngine,
						"envPrefix":       spicedbEnvPrefix,
						"cmd":             spicedbCmd,
						"tlsSecretName":   "spicedb-grpc-tls",
						"image":           "spicedb:dev",
					}
					for k, v := range dsDef.passthroughConfig {
						config[k] = v
					}
					jsonConfig, err := json.Marshal(config)
					Expect(err).To(BeNil())
					spiceCluster = &v1alpha1.SpiceDBCluster{
						TypeMeta: metav1.TypeMeta{
							Kind:       v1alpha1.SpiceDBClusterKind,
							APIVersion: v1alpha1.SchemeGroupVersion.String(),
						},
						ObjectMeta: metav1.ObjectMeta{
							Name:      fmt.Sprintf("test2-%s", dsDef.label),
							Namespace: testNamespace,
						},
						Spec: v1alpha1.ClusterSpec{
							Config:    jsonConfig,
							SecretRef: "spicedb3",
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
					DeferCleanup(kclient.CoreV1().Secrets(spiceCluster.Namespace).Delete, ctx, tlsSecret.Name, metav1.DeleteOptions{})

					secret := corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "spicedb3",
							Namespace: spiceCluster.Namespace,
						},
						StringData: map[string]string{
							"datastore_uri":     dsDef.datastoreUri,
							"migration_secrets": "kaitain-bootstrap-token=testtesttesttest,sharewith-bootstrap-token=testtesttesttest,thumper-bootstrap-token=testtesttesttest,metrics-proxy-token=testtesttesttest",
							"preshared_key":     "testtesttesttest",
						},
					}
					_, err = kclient.CoreV1().Secrets(spiceCluster.Namespace).Create(ctx, &secret, metav1.CreateOptions{})
					Expect(err).To(Succeed())
					DeferCleanup(kclient.CoreV1().Secrets(spiceCluster.Namespace).Delete, ctx, secret.Name, metav1.DeleteOptions{})

					u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(spiceCluster)
					Expect(err).To(Succeed())
					_, err = client.Resource(v1alpha1ClusterGVR).Namespace(spiceCluster.Namespace).Create(ctx, &unstructured.Unstructured{Object: u}, metav1.CreateOptions{})
					Expect(err).To(Succeed())

					AssertMigrationsCompleted("spicedb:dev", "", "",
						func() (string, string, string) {
							return spiceCluster.Namespace, spiceCluster.Name, dsDef.datastoreEngine
						})
				})

				AfterAll(func() {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()

					newConfig := config.OperatorConfig{
						ImageName: "spicedb",
						ImageTag:  "dev",
					}
					WriteConfig(newConfig)

					Expect(client.Resource(v1alpha1ClusterGVR).Namespace(spiceCluster.Namespace).Delete(ctx, spiceCluster.Name, metav1.DeleteOptions{})).To(Succeed())

					AssertDependentResourceCleanup(spiceCluster.Namespace, spiceCluster.Name, "spicedb3")
				})

				Describe("with a migrated datastore", func() {
					BeforeEach(func() {
						AssertHealthySpiceDBCluster("spicedb:dev",
							func() (string, string) {
								return testNamespace, spiceCluster.Name
							}, Not(ContainSubstring("ERROR: kuberesolver")))
					})

					When("the image is updated but there is a required edge", func() {
						BeforeEach(func() {
							ctx, cancel := context.WithCancel(context.Background())
							DeferCleanup(cancel)

							clusterUnst, err := client.Resource(v1alpha1ClusterGVR).Namespace(spiceCluster.Namespace).Get(ctx, spiceCluster.Name, metav1.GetOptions{})
							Expect(err).To(Succeed())
							cluster, err := typed.UnstructuredObjToTypedObj[*v1alpha1.SpiceDBCluster](clusterUnst)
							Expect(err).To(Succeed())

							config := map[string]any{
								"datastoreEngine": dsDef.datastoreEngine,
								"envPrefix":       spicedbEnvPrefix,
								"cmd":             spicedbCmd,
								"tlsSecretName":   "spicedb-grpc-tls",
								"image":           "spicedb:next",
							}
							for k, v := range dsDef.passthroughConfig {
								config[k] = v
							}
							jsonConfig, err := json.Marshal(config)
							Expect(err).To(Succeed())
							cluster.Spec.Config = jsonConfig
							u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cluster)
							Expect(err).To(Succeed())
							_, err = client.Resource(v1alpha1ClusterGVR).Namespace(cluster.Namespace).Update(ctx, &unstructured.Unstructured{Object: u}, metav1.UpdateOptions{})
							Expect(err).To(Succeed())
						})

						It("migrates to the required edge", func() {
							AssertMigrationsCompleted("spicedb:updated", migration, "phase2",
								func() (string, string, string) {
									return spiceCluster.Namespace, spiceCluster.Name, dsDef.datastoreEngine
								})
						})

						Describe("after it has updated to the required edge", func() {
							It("migrates to the desired image", func() {
								AssertMigrationsCompleted("spicedb:next", "", "",
									func() (string, string, string) {
										return spiceCluster.Namespace, spiceCluster.Name, dsDef.datastoreEngine
									})
							})
						})
					})
				})
			})
		})
	}
})
