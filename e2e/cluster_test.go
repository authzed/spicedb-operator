//go:build e2e

package e2e

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
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
	batchv1 "k8s.io/api/batch/v1"
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

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/config"
	"github.com/authzed/spicedb-operator/pkg/metadata"
	"github.com/authzed/spicedb-operator/pkg/updates"
)

var (
	v1alpha1ClusterGVR = v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.SpiceDBClusterResourceName)

	spicedbEnvPrefix = stringz.DefaultEmpty(os.Getenv("SPICEDB_ENV_PREFIX"), "SPICEDB")
	spicedbCmd       = stringz.DefaultEmpty(os.Getenv("SPICEDB_CMD"), "spicedb")

	noop = func(namespace string) error { return nil }

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
		passthroughConfig: map[string]string{
			"datastoreSpannerEmulatorHost": "spanner-service:9010",
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
	},
	{
		description:     "With Postgresql",
		label:           "postgresql",
		definition:      postgresqlyaml,
		definedObjs:     2,
		datastoreUri:    "postgresql://postgres:testpassword@postgresql-db-public:5432/postgres?sslmode=disable",
		datastoreEngine: "postgres",
		datastoreSetup:  noop,
	},
	{
		description:     "With MySQL",
		label:           "mysql",
		definition:      mysqlyaml,
		definedObjs:     2,
		datastoreUri:    "root:password@tcp(mysql-public:3306)/mysql?parseTime=true",
		datastoreEngine: "mysql",
		datastoreSetup:  noop,
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

	AssertServiceAccount := func(name string, annotations map[string]string, args func() (string, string)) {
		namespace, owner := args()
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)

		var serviceAccounts *corev1.ServiceAccountList
		Eventually(func(g Gomega) {
			var err error
			serviceAccounts, err = kclient.CoreV1().ServiceAccounts(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", metadata.ComponentLabelKey, metadata.ComponentServiceAccountLabel, metadata.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(serviceAccounts.Items)).To(Equal(1))
			g.Expect(serviceAccounts.Items[0].GetName()).To(Equal(name))
			for k, v := range annotations {
				g.Expect(serviceAccounts.Items[0].GetAnnotations()).To(HaveKeyWithValue(k, v))
			}
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
		Eventually(func(g Gomega) {
			watchCtx, watchCancel := context.WithTimeout(ctx, 3*time.Minute)
			defer watchCancel()
			Watch(watchCtx, client, v1alpha1ClusterGVR, ktypes.NamespacedName{Name: name, Namespace: namespace}, "0", func(c *v1alpha1.SpiceDBCluster) bool {
				condition = c.FindStatusCondition("Migrating")
				GinkgoWriter.Println(c.Status)
				return condition == nil
			})
			g.Expect(condition).To(EqualCondition(v1alpha1.NewMigratingCondition(datastoreEngine, migration)))
		}).Should(Succeed())

		var job *batchv1.Job
		watchCtx, watchCancel := context.WithTimeout(ctx, 3*time.Minute)
		defer watchCancel()
		watcher, err := kclient.BatchV1().Jobs(namespace).Watch(watchCtx, metav1.ListOptions{
			Watch:           true,
			ResourceVersion: "0",
			LabelSelector:   fmt.Sprintf("%s=%s", metadata.ComponentLabelKey, metadata.ComponentMigrationJobLabelValue),
		})
		Expect(err).To(Succeed())

		matchingJob := false
		for event := range watcher.ResultChan() {
			job = event.Object.(*batchv1.Job)
			GinkgoWriter.Println(job)

			if job.Spec.Template.Spec.Containers[0].Image != image {
				GinkgoWriter.Println("expected job image doesn't match")
				continue
			}

			if !strings.Contains(strings.Join(job.Spec.Template.Spec.Containers[0].Command, " "), migration) {
				GinkgoWriter.Println("expected job migration doesn't match")
				continue
			}
			if phase != "" {
				foundPhase := false
				for _, e := range job.Spec.Template.Spec.Containers[0].Env {
					GinkgoWriter.Println(e)
					if e.Value == phase {
						foundPhase = true
					}
				}
				if !foundPhase {
					GinkgoWriter.Println("expected job phase doesn't match")
					continue
				}
			}

			// found a matching job
			TailF(job)

			// wait for job to succeed
			if job.Status.Succeeded == 0 {
				GinkgoWriter.Println("job hasn't succeeded")
				continue
			}
			matchingJob = true
			break
		}
		Expect(matchingJob).To(BeTrue())
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
					Expect(condition).To(EqualCondition(v1alpha1.NewInvalidConfigCondition("", fmt.Errorf("[datastoreEngine is a required field, couldn't find channel for datastore \"\": no channel found for datastore \"\", no update found in channel, secret must be provided]"))))
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
					g.Expect(out.Status.ReadyReplicas).To(Equal(*out.Spec.Replicas))
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
						"image":           "spicedb:dev",
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
						"image":           "spicedb:dev",
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

			When("a valid SpiceDBCluster is created (with TLS and non-default Service Account)", Ordered, func() {
				var spiceCluster *v1alpha1.SpiceDBCluster

				BeforeAll(func() {
					ctx, cancel := context.WithCancel(context.Background())
					DeferCleanup(cancel)

					config := map[string]any{
						"datastoreEngine":                dsDef.datastoreEngine,
						"envPrefix":                      spicedbEnvPrefix,
						"cmd":                            spicedbCmd,
						"image":                          "spicedb:dev",
						"tlsSecretName":                  "spicedb-grpc-tls",
						"dispatchUpstreamCASecretName":   "spicedb-grpc-tls",
						"serviceAccountName":             "spicedb-non-default",
						"extraServiceAccountAnnotations": "authzed.com/e2e=true",
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

					tlsSecret := GenerateCertManagerCompliantTLSSecretForService(
						ktypes.NamespacedName{Name: spiceCluster.Name, Namespace: spiceCluster.Namespace},
						ktypes.NamespacedName{Name: "spicedb-grpc-tls", Namespace: spiceCluster.Namespace},
					)
					_, err = kclient.CoreV1().Secrets(spiceCluster.Namespace).Create(ctx, tlsSecret, metav1.CreateOptions{})
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

					It("creates the service account", func() {
						annotations := map[string]string{"authzed.com/e2e": "true"}
						AssertServiceAccount("spicedb-non-default", annotations, func() (string, string) {
							return testNamespace, spiceCluster.Name
						})
					})

					When("the spicedb cluster is running", func() {
						It("cleans up the migration job", func() {
							AssertMigrationJobCleanup(func() (string, string) {
								return spiceCluster.Namespace, spiceCluster.Name
							})
						})
					})
				})
			})
		})
	}

	Describe("there is a series of required migrations", Ordered, Label("postgresql"), func() {
		var testNamespace string
		var spiceCluster *v1alpha1.SpiceDBCluster

		BeforeAll(func() {
			testNamespace = "test-postgres-migrations"
			CreateNamespace(testNamespace)
			DeferCleanup(DeleteNamespace, testNamespace)

			dc, err := discovery.NewDiscoveryClientForConfig(restConfig)
			Expect(err).To(Succeed())
			mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))
			decoder := yaml.NewYAMLToJSONDecoder(io.NopCloser(bytes.NewReader(postgresqlyaml)))
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
			Expect(len(objs)).To(Equal(2))

			By("waiting for pg to start...")
			Eventually(func(g Gomega) {
				out, err := kclient.AppsV1().StatefulSets(testNamespace).Get(context.Background(), db.GetName(), metav1.GetOptions{})
				g.Expect(err).To(Succeed())
				g.Expect(out.Status.ReadyReplicas).To(Equal(*out.Spec.Replicas))
			}).Should(Succeed())
			Eventually(func(g Gomega) {
				out, err := kclient.CoreV1().Pods(testNamespace).Get(context.Background(), db.GetName()+"-0", metav1.GetOptions{})
				g.Expect(err).To(Succeed())
				g.Expect(out.Status.Phase).To(Equal(corev1.PodRunning))
			}).Should(Succeed())
			By("pg running.")

			ctx, cancel := context.WithCancel(context.Background())
			DeferCleanup(cancel)

			newConfig := config.OperatorConfig{
				ImageName: "ghcr.io/authzed/spicedb",
				UpdateGraph: updates.UpdateGraph{
					Channels: []updates.Channel{
						{
							Name:     "postgres",
							Metadata: map[string]string{"datastore": "postgres"},
							Nodes: []updates.State{
								{ID: "v1.14.1", Tag: "v1.14.1", Migration: "drop-id-constraints"},
								{ID: "v1.14.0", Tag: "v1.14.0", Migration: "drop-id-constraints"},
								{ID: "v1.14.0-phase2", Tag: "v1.14.0", Migration: "add-xid-constraints", Phase: "write-both-read-new"},
								{ID: "v1.14.0-phase1", Tag: "v1.14.0", Migration: "add-xid-columns", Phase: "write-both-read-old"},
								{ID: "v1.13.0", Tag: "v1.13.0", Migration: "add-ns-config-id"},
							},
							Edges: map[string][]string{
								"v1.13.0":        {"v1.14.0-phase1"},
								"v1.14.0-phase1": {"v1.14.0-phase2"},
								"v1.14.0-phase2": {"v1.14.0"},
								"v1.14.0":        {"v1.14.1"},
							},
						},
					},
				},
			}
			WriteConfig(newConfig)

			classConfig := map[string]any{
				"logLevel":                     "debug",
				"datastoreEngine":              "postgres",
				"tlsSecretName":                "spicedb4-grpc-tls",
				"dispatchUpstreamCASecretName": "spicedb4-grpc-tls",
			}
			jsonConfig, err := json.Marshal(classConfig)
			Expect(err).To(BeNil())
			spiceCluster = &v1alpha1.SpiceDBCluster{
				TypeMeta: metav1.TypeMeta{
					Kind:       v1alpha1.SpiceDBClusterKind,
					APIVersion: v1alpha1.SchemeGroupVersion.String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("test4-postgresql"),
					Namespace: testNamespace,
				},
				Spec: v1alpha1.ClusterSpec{
					Version:   "v1.13.0",
					Config:    jsonConfig,
					SecretRef: "spicedb4",
				},
			}

			tlsSecret := GenerateCertManagerCompliantTLSSecretForService(
				ktypes.NamespacedName{Name: spiceCluster.Name, Namespace: spiceCluster.Namespace},
				ktypes.NamespacedName{Name: "spicedb4-grpc-tls", Namespace: spiceCluster.Namespace},
			)
			_, err = kclient.CoreV1().Secrets(spiceCluster.Namespace).Create(ctx, tlsSecret, metav1.CreateOptions{})
			Expect(err).To(Succeed())
			DeferCleanup(kclient.CoreV1().Secrets(spiceCluster.Namespace).Delete, ctx, tlsSecret.Name, metav1.DeleteOptions{})

			secret := corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "spicedb4",
					Namespace: spiceCluster.Namespace,
				},
				StringData: map[string]string{
					"datastore_uri":     "postgresql://postgres:testpassword@postgresql-db-public:5432/postgres?sslmode=disable",
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

			AssertMigrationsCompleted("ghcr.io/authzed/spicedb:v1.13.0", "add-ns-config-id", "",
				func() (string, string, string) {
					return spiceCluster.Namespace, spiceCluster.Name, "postgres"
				})

			Eventually(func(g Gomega) {
				clusterUnst, err := client.Resource(v1alpha1ClusterGVR).Namespace(spiceCluster.Namespace).Get(ctx, spiceCluster.Name, metav1.GetOptions{})
				g.Expect(err).To(Succeed())
				fetched, err := typed.UnstructuredObjToTypedObj[*v1alpha1.SpiceDBCluster](clusterUnst)
				g.Expect(err).To(Succeed())
				g.Expect(len(fetched.Status.Conditions)).To(BeZero())
				GinkgoWriter.Println(fetched.Status)
				g.Expect(len(fetched.Status.AvailableVersions)).ToNot(BeZero(), "status should show available updates")
			}).Should(Succeed())

			// once the cluster is running at the initial version, update the target version
			Eventually(func(g Gomega) {
				clusterUnst, err := client.Resource(v1alpha1ClusterGVR).Namespace(spiceCluster.Namespace).Get(ctx, spiceCluster.Name, metav1.GetOptions{})
				g.Expect(err).To(Succeed())
				fetched, err := typed.UnstructuredObjToTypedObj[*v1alpha1.SpiceDBCluster](clusterUnst)
				g.Expect(err).To(Succeed())
				g.Expect(len(fetched.Status.Conditions)).To(BeZero())

				fetched.Spec.Version = "v1.14.1"
				u, err = runtime.DefaultUnstructuredConverter.ToUnstructured(fetched)
				Expect(err).To(Succeed())
				_, err = client.Resource(v1alpha1ClusterGVR).Namespace(spiceCluster.Namespace).Update(ctx, &unstructured.Unstructured{Object: u}, metav1.UpdateOptions{})
				Expect(err).To(Succeed())
			}).Should(Succeed())
		})

		AfterAll(func() {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			newConfig := config.OperatorConfig{
				ImageName: "spicedb",
			}
			WriteConfig(newConfig)

			Expect(client.Resource(v1alpha1ClusterGVR).Namespace(spiceCluster.Namespace).Delete(ctx, spiceCluster.Name, metav1.DeleteOptions{})).To(Succeed())

			AssertDependentResourceCleanup(spiceCluster.Namespace, spiceCluster.Name, "spicedb4")
		})

		When("there is a series of required migrations", Ordered, func() {
			It("migrates to phase1", func() {
				AssertMigrationsCompleted("ghcr.io/authzed/spicedb:v1.14.0", "add-xid-columns", "write-both-read-old",
					func() (string, string, string) {
						return spiceCluster.Namespace, spiceCluster.Name, "postgres"
					})
			})

			It("migrates to phase2", func() {
				AssertMigrationsCompleted("ghcr.io/authzed/spicedb:v1.14.0", "add-xid-constraints", "write-both-read-new",
					func() (string, string, string) {
						return spiceCluster.Namespace, spiceCluster.Name, "postgres"
					})
			})

			It("migrates to phase3", func() {
				AssertMigrationsCompleted("ghcr.io/authzed/spicedb:v1.14.0", "drop-id-constraints", "",
					func() (string, string, string) {
						return spiceCluster.Namespace, spiceCluster.Name, "postgres"
					})
			})

			It("updates to the next version", func() {
				AssertHealthySpiceDBCluster("ghcr.io/authzed/spicedb:v1.14.1",
					func() (string, string) {
						return spiceCluster.Namespace, spiceCluster.Name
					}, Not(ContainSubstring("ERROR: kuberesolver")))
			})
		})
	})
})
