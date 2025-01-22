//go:build e2e

package e2e

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/authzed/controller-idioms/typed"
	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/jzelinskie/stringz"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ktypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"

	"github.com/authzed/spicedb-operator/e2e/databases"
	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/metadata"
	"github.com/authzed/spicedb-operator/pkg/updates"
)

var (
	v1alpha1ClusterGVR = v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.SpiceDBClusterResourceName)

	spicedbEnvPrefix = stringz.DefaultEmpty(os.Getenv("SPICEDB_ENV_PREFIX"), "SPICEDB")
	spicedbCmd       = stringz.DefaultEmpty(os.Getenv("SPICEDB_CMD"), "spicedb")
)

var _ = Describe("SpiceDBClusters", func() {
	var (
		client        dynamic.Interface
		kclient       kubernetes.Interface
		mapper        meta.RESTMapper
		testNamespace string
		image         string
		config        map[string]any
		secret        *corev1.Secret
		cluster       *v1alpha1.SpiceDBCluster

		ctx = logr.NewContext(context.Background(), funcr.New(func(prefix, args string) {
			GinkgoWriter.Println(prefix, args)
		}, funcr.Options{}))

		AssertMigrationJobCleanup      func(owner string)
		AssertServiceAccount           func(name string, annotations map[string]string, owner string)
		AssertHealthySpiceDBCluster    func(image, owner string, logMatcher types.GomegaMatcher)
		AssertDependentResourceCleanup func(owner, secretName string)
		AssertMigrationsCompleted      func(image, migration, phase, name, datastoreEngine string)
	)

	BeforeEach(func() {
		c, err := dynamic.NewForConfig(restConfig)
		Expect(err).To(Succeed())
		client = c

		k, err := kubernetes.NewForConfig(restConfig)
		Expect(err).To(Succeed())
		kclient = k

		dc, err := discovery.NewDiscoveryClientForConfig(restConfig)
		Expect(err).To(Succeed())
		mapper = restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))

		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)
		ns, err := k.CoreV1().Namespaces().Create(
			ctx,
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-"}},
			metav1.CreateOptions{})
		Expect(err).To(Succeed())
		testNamespace = ns.Name
		DeferCleanup(k.CoreV1().Namespaces().Delete, ctx, testNamespace, metav1.DeleteOptions{})

		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "spicedb",
				Namespace: testNamespace,
			},
			StringData: map[string]string{
				"logLevel":          "debug",
				"preshared_key":     "testtesttesttest",
				"migration_secrets": "kaitain-bootstrap-token=testtesttesttest,sharewith-bootstrap-token=testtesttesttest,thumper-bootstrap-token=testtesttesttest,metrics-proxy-token=testtesttesttest",
			},
		}

		config = map[string]any{
			"envPrefix":         spicedbEnvPrefix,
			"image":             image,
			"cmd":               spicedbCmd,
			"skipReleaseCheck":  "true",
			"telemetryEndpoint": "",
		}

		cluster = &v1alpha1.SpiceDBCluster{
			TypeMeta: metav1.TypeMeta{
				Kind:       v1alpha1.SpiceDBClusterKind,
				APIVersion: v1alpha1.SchemeGroupVersion.String(),
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: testNamespace,
			},
			Spec: v1alpha1.ClusterSpec{
				SecretRef: "spicedb",
			},
		}

		AssertMigrationJobCleanup = AssertMigrationJobCleanupFunc(ctx, testNamespace, kclient)
		AssertServiceAccount = AssertServiceAccountFunc(ctx, testNamespace, kclient)
		AssertHealthySpiceDBCluster = AssertHealthySpiceDBClusterFunc(ctx, testNamespace, kclient)
		AssertDependentResourceCleanup = AssertDependentResourceCleanupFunc(ctx, testNamespace, kclient)
		AssertMigrationsCompleted = AssertMigrationsCompletedFunc(ctx, testNamespace, kclient, client)
	})

	JustBeforeEach(func() {
		// cluster and secret are created "just before" specs, so that the definitions can
		// be modified by nested BeforeEach blocks.
		_, err := kclient.CoreV1().Secrets(testNamespace).Create(ctx, secret, metav1.CreateOptions{})
		Expect(err).To(Succeed())

		jsonConfig, err := json.Marshal(config)
		Expect(err).To(Succeed())
		cluster.Spec.Config = jsonConfig
		u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cluster)
		Expect(err).To(Succeed())
		_, err = client.Resource(v1alpha1ClusterGVR).Namespace(cluster.Namespace).Create(ctx, &unstructured.Unstructured{Object: u}, metav1.CreateOptions{})
		Expect(err).To(Succeed())
	})

	JustAfterEach(func() {
		// TODO: this wasn't working in DeferCleanup (AfterEach blocks were running before DeferCleanup in JustBeforeEach)
		Expect(client.Resource(v1alpha1ClusterGVR).Namespace(cluster.Namespace).Delete(context.Background(), cluster.Name, metav1.DeleteOptions{})).To(Succeed())
	})

	Describe("With invalid config", func() {
		BeforeEach(func() {
			cluster.Spec.Config = []byte("{}")
			cluster.Spec.SecretRef = ""
		})

		It("Reports invalid config on the status", func() {
			ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			DeferCleanup(cancel)
			var condition *metav1.Condition
			Watch(ctx, client, v1alpha1ClusterGVR, ktypes.NamespacedName{Name: cluster.Name, Namespace: testNamespace}, "0", func(c *v1alpha1.SpiceDBCluster) bool {
				condition = c.FindStatusCondition(v1alpha1.ConditionValidatingFailed)
				logr.FromContextOrDiscard(ctx).Info("watch event", "status", c.Status)
				return condition == nil
			})
			Expect(condition).To(EqualCondition(v1alpha1.NewInvalidConfigCondition("", fmt.Errorf("[datastoreEngine is a required field, couldn't find channel for datastore \"\": no channel found for datastore \"\", no update found in channel, secret must be provided]"))))
		})
	})

	Describe("With missing secret", func() {
		var condition *metav1.Condition
		var rv string

		BeforeEach(func() {
			cluster.Spec.Config = []byte(`{"datastoreEngine": "memory"}`)
			cluster.Spec.SecretRef = "nonexistent"
		})

		JustBeforeEach(func() {
			// The top-level JustBeforeEach creates the cluster, so we need to start the watch
			// in a nested JustBeforeEach instead of a BeforeEach
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			DeferCleanup(cancel)
			Watch(ctx, client, v1alpha1ClusterGVR, ktypes.NamespacedName{Name: cluster.Name, Namespace: testNamespace}, "0", func(c *v1alpha1.SpiceDBCluster) bool {
				condition = c.FindStatusCondition(v1alpha1.ConditionTypePreconditionsFailed)
				logr.FromContextOrDiscard(ctx).Info("watch event", "status", c.Status)
				rv = c.ResourceVersion
				return condition == nil
			})
		})

		It("Reports missing secret on the status", func() {
			Expect(condition).To(EqualCondition(v1alpha1.NewMissingSecretCondition(ktypes.NamespacedName{
				Namespace: testNamespace,
				Name:      "nonexistent",
			})))
		})

		Describe("when the secret is created", func() {
			var newSecret *corev1.Secret

			BeforeEach(func() {
				newSecret = secret.DeepCopy()
				newSecret.Name = "nonexistent"
			})

			JustBeforeEach(func() {
				_, err := kclient.CoreV1().Secrets(testNamespace).Create(ctx, newSecret, metav1.CreateOptions{})
				Expect(err).To(Succeed())
			})

			It("removes the missing secret condition", func() {
				ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				// starts watching events at resourceversion where the last watch ended
				Watch(ctx, client, v1alpha1ClusterGVR, ktypes.NamespacedName{Name: cluster.Name, Namespace: testNamespace}, rv, func(c *v1alpha1.SpiceDBCluster) bool {
					logr.FromContextOrDiscard(ctx).Info("watch event", "status", c.Status)
					return c.FindStatusCondition(v1alpha1.ConditionTypePreconditionsFailed) != nil
				})
			})
		})
	})

	Describe("With a database", func() {
		var db *databases.LogicalDatabase

		BasicSpiceDBFunctionality := func() {
			When("a valid SpiceDBCluster is created", func() {
				BeforeEach(func() {
					secret.StringData = map[string]string{
						"logLevel":          "debug",
						"datastore_uri":     db.DatastoreURI,
						"preshared_key":     "testtesttesttest",
						"migration_secrets": "kaitain-bootstrap-token=testtesttesttest,sharewith-bootstrap-token=testtesttesttest,thumper-bootstrap-token=testtesttesttest,metrics-proxy-token=testtesttesttest",
					}

					config = map[string]any{
						"skipReleaseCheck":              "true",
						"telemetryEndpoint":             "",
						"datastoreEngine":               db.Engine,
						"datastoreConnpoolReadMinOpen":  1,
						"datastoreConnpoolWriteMinOpen": 1,
						"envPrefix":                     spicedbEnvPrefix,
						"image":                         image,
						"cmd":                           spicedbCmd,
					}
					for k, v := range db.ExtraConfig {
						config[k] = v
					}
				})

				JustBeforeEach(func() {
					AssertMigrationsCompleted(image, "", "", cluster.Name, db.Engine)
				})

				AfterEach(func() {
					AssertDependentResourceCleanup(cluster.Name, "spicedb")
				})

				It("creates a spicedb cluster", func() {
					AssertHealthySpiceDBCluster(image, cluster.Name, Not(ContainSubstring("ERROR: kuberesolver")))

					By("cleaning up the migration job")
					AssertMigrationJobCleanup(cluster.Name)
				})

				When("options are specified (TLS, ServiceAccount, default channel)", func() {
					BeforeEach(func() {
						// this installs from the head of the current channel, skip validating image
						image = ""
						config = map[string]any{
							"skipReleaseCheck":               true,
							"telemetryEndpoint":              "",
							"datastoreEngine":                db.Engine,
							"envPrefix":                      spicedbEnvPrefix,
							"cmd":                            spicedbCmd,
							"tlsSecretName":                  "spicedb-grpc-tls",
							"dispatchUpstreamCASecretName":   "spicedb-grpc-tls",
							"dispatchUpstreamCAFilePath":     "ca.crt",
							"serviceAccountName":             "spicedb-non-default",
							"extraServiceAccountAnnotations": "authzed.com/e2e=true",
							"datastoreConnpoolReadMinOpen":   1,
							"datastoreConnpoolWriteMinOpen":  1,
						}
						for k, v := range db.ExtraConfig {
							config[k] = v
						}
						cluster.Spec.Patches = []v1alpha1.Patch{{
							Kind: "Deployment",
							Patch: json.RawMessage(`{
							  "metadata": {
								"labels": {
								  "added": "via-patch"
								}
							  }
							}`),
						}}

						tlsSecret := GenerateCertManagerCompliantTLSSecretForService(
							ktypes.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace},
							ktypes.NamespacedName{Name: "spicedb-grpc-tls", Namespace: cluster.Namespace},
						)
						_, err := kclient.CoreV1().Secrets(cluster.Namespace).Create(ctx, tlsSecret, metav1.CreateOptions{})
						Expect(err).To(Succeed())
						DeferCleanup(kclient.CoreV1().Secrets(cluster.Namespace).Delete, ctx, tlsSecret.Name, metav1.DeleteOptions{})
					})

					It("creates with those options", func() {
						ctx, cancel := context.WithTimeout(ctx, 1*time.Minute)
						DeferCleanup(cancel)

						By("not having warnings")
						var conditions []metav1.Condition
						Watch(ctx, client, v1alpha1ClusterGVR, ktypes.NamespacedName{Name: cluster.Name, Namespace: testNamespace}, "0", func(c *v1alpha1.SpiceDBCluster) bool {
							conditions = c.Status.Conditions
							logr.FromContextOrDiscard(ctx).Info("watch event", "status", c.Status)
							return len(conditions) > 0
						})
						Expect(conditions).To(BeEmpty())

						By("creating the serviceaccount")
						AssertServiceAccount("spicedb-non-default", map[string]string{"authzed.com/e2e": "true"}, cluster.Name)
					})
				})
			})

			When("a valid spicedb cluster with a pod error", func() {
				BeforeEach(func() {
					secret.StringData = map[string]string{
						"datastore_uri":     db.DatastoreURI,
						"preshared_key":     "testtesttesttest",
						"migration_secrets": "kaitain-bootstrap-token=testtesttesttest,sharewith-bootstrap-token=testtesttesttest,thumper-bootstrap-token=testtesttesttest,metrics-proxy-token=testtesttesttest",
					}
					config = map[string]any{
						"skipMigrations":                true,
						"datastoreEngine":               db.Engine,
						"image":                         image,
						"envPrefix":                     spicedbEnvPrefix,
						"cmd":                           "badcmd",
						"datastoreConnpoolReadMinOpen":  "1",
						"datastoreConnpoolWriteMinOpen": "1",
					}
					for k, v := range db.ExtraConfig {
						config[k] = v
					}
				})

				It("reports un-recovered pod errors on the status", func() {
					var lastCluster *v1alpha1.SpiceDBCluster
					var condition *metav1.Condition
					Watch(ctx, client, v1alpha1ClusterGVR, ktypes.NamespacedName{Name: cluster.Name, Namespace: testNamespace}, "0", func(c *v1alpha1.SpiceDBCluster) bool {
						condition = c.FindStatusCondition(v1alpha1.ConditionTypeRolloutError)
						logr.FromContextOrDiscard(ctx).Info("watch event", "status", c.Status)
						lastCluster = c
						return condition == nil
					})
					Expect(condition).To(
						// different kube versions return different messages, this matches either
						Or(
							EqualCondition(v1alpha1.NewPodErrorCondition(`failed to create containerd task: failed to create shim task: OCI runtime create failed: runc create failed: unable to start container process: error during container init: exec: "badcmd": executable file not found in $PATH: unknown`)),
							EqualCondition(v1alpha1.NewPodErrorCondition(`failed to create containerd task: failed to create shim task: OCI runtime create failed: runc create failed: unable to start container process: exec: "badcmd": executable file not found in $PATH: unknown`)),
						),
					)

					By("fixing the config problem")
					config["cmd"] = spicedbCmd
					config["skipMigrations"] = "false"
					jsonConfig, err := json.Marshal(config)
					Expect(err).To(Succeed())
					lastCluster.Spec.Config = jsonConfig
					u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(lastCluster)
					Expect(err).To(Succeed())
					_, err = client.Resource(v1alpha1ClusterGVR).Namespace(cluster.Namespace).Update(ctx, &unstructured.Unstructured{Object: u}, metav1.UpdateOptions{})
					Expect(err).To(Succeed())

					By("the condition should be removed")
					Watch(ctx, client, v1alpha1ClusterGVR, ktypes.NamespacedName{Name: cluster.Name, Namespace: testNamespace}, "0", func(c *v1alpha1.SpiceDBCluster) bool {
						condition = c.FindStatusCondition(v1alpha1.ConditionTypeRolloutError)
						logr.FromContextOrDiscard(ctx).Info("watch event", "status", c.Status)
						return condition != nil
					})
					Expect(condition).To(BeNil())
				})
			})

			When("a valid SpiceDBCluster and skipped migrations", func() {
				BeforeEach(func() {
					secret.StringData = map[string]string{
						"logLevel":          "debug",
						"datastore_uri":     db.DatastoreURI,
						"preshared_key":     "testtesttesttest",
						"migration_secrets": "kaitain-bootstrap-token=testtesttesttest,sharewith-bootstrap-token=testtesttesttest,thumper-bootstrap-token=testtesttesttest,metrics-proxy-token=testtesttesttest",
					}
					config = map[string]any{
						"skipMigrations":                true,
						"datastoreEngine":               db.Engine,
						"image":                         image,
						"envPrefix":                     spicedbEnvPrefix,
						"cmd":                           spicedbCmd,
						"datastoreConnpoolReadMinOpen":  "1",
						"datastoreConnpoolWriteMinOpen": "1",
					}
				})

				It("Starts SpiceDB without migrating", func() {
					Consistently(func(g Gomega) {
						jobs, err := kclient.BatchV1().Jobs(testNamespace).List(ctx, metav1.ListOptions{})
						g.Expect(err).To(Succeed())
						g.Expect(len(jobs.Items)).To(BeZero())
					}).Should(Succeed())

					var deps *appsv1.DeploymentList
					Eventually(func(g Gomega) {
						var err error
						deps, err = kclient.AppsV1().Deployments(testNamespace).List(ctx, metav1.ListOptions{
							LabelSelector: fmt.Sprintf("%s=%s,%s=%s", metadata.ComponentLabelKey, metadata.ComponentSpiceDBLabelValue, metadata.OwnerLabelKey, cluster.Name),
						})
						g.Expect(err).To(Succeed())
						g.Expect(len(deps.Items)).To(Equal(1))
						logr.FromContextOrDiscard(ctx).Info("deployment", "name", deps.Items[0].Name)
					}).Should(Succeed())
				})
			})
		}

		UpdateTest := func(engine, channel, from, to string) {
			Describe(fmt.Sprintf("using channel %s, from %s to %s", channel, from, to), func() {
				BeforeEach(func() {
					secret.StringData = map[string]string{
						"logLevel":          "debug",
						"datastore_uri":     db.DatastoreURI,
						"preshared_key":     "testtesttesttest",
						"migration_secrets": "kaitain-bootstrap-token=testtesttesttest,sharewith-bootstrap-token=testtesttesttest,thumper-bootstrap-token=testtesttesttest,metrics-proxy-token=testtesttesttest",
					}

					config = map[string]any{
						"skipReleaseCheck":              "true",
						"telemetryEndpoint":             "",
						"datastoreEngine":               engine,
						"envPrefix":                     spicedbEnvPrefix,
						"cmd":                           spicedbCmd,
						"datastoreConnpoolReadMinOpen":  1,
						"datastoreConnpoolWriteMinOpen": 1,
					}
					for k, v := range db.ExtraConfig {
						config[k] = v
					}
					for _, s := range graphExtraConfig {
						k, v, ok := strings.Cut(s, "=")
						if !ok {
							continue
						}
						config[k] = v
					}
					cluster.Spec.Channel = channel
					cluster.Spec.Version = from
				})

				JustBeforeEach(func() {
					Eventually(func(g Gomega) {
						clusterUnst, err := client.Resource(v1alpha1ClusterGVR).Namespace(cluster.Namespace).Get(ctx, cluster.Name, metav1.GetOptions{})
						g.Expect(err).To(Succeed())
						fetched, err := typed.UnstructuredObjToTypedObj[*v1alpha1.SpiceDBCluster](clusterUnst)
						g.Expect(err).To(Succeed())
						logr.FromContextOrDiscard(ctx).Info("fetched cluster", "status", fetched.Status)
						g.Expect(fetched.Status.CurrentVersion).ToNot(BeNil())
						g.Expect(fetched.Status.CurrentVersion.Name).To(Equal(from))
						meta.RemoveStatusCondition(&fetched.Status.Conditions, v1alpha1.ConditionTypeConfigWarnings)
						g.Expect(len(fetched.Status.Conditions)).To(BeZero())
						g.Expect(len(fetched.Status.AvailableVersions)).ToNot(BeZero(), "status should show available updates")
						// TODO: validate the target version is in the available version list
					}).Should(Succeed())

					// once the cluster is running at the initial version, update the target version
					Eventually(func(g Gomega) {
						clusterUnst, err := client.Resource(v1alpha1ClusterGVR).Namespace(cluster.Namespace).Get(ctx, cluster.Name, metav1.GetOptions{})
						g.Expect(err).To(Succeed())
						fetched, err := typed.UnstructuredObjToTypedObj[*v1alpha1.SpiceDBCluster](clusterUnst)
						g.Expect(err).To(Succeed())
						meta.RemoveStatusCondition(&fetched.Status.Conditions, v1alpha1.ConditionTypeConfigWarnings)
						g.Expect(len(fetched.Status.Conditions)).To(BeZero())

						fetched.Spec.Version = to
						u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(fetched)
						g.Expect(err).To(Succeed())
						_, err = client.Resource(v1alpha1ClusterGVR).Namespace(cluster.Namespace).Update(ctx, &unstructured.Unstructured{Object: u}, metav1.UpdateOptions{})
						g.Expect(err).To(Succeed())
					}).Should(Succeed())
				})

				AfterEach(func() {
					AssertDependentResourceCleanup(cluster.Name, "spicedb")
				})

				It("should update with no issues", func() {
					Eventually(func(g Gomega) {
						clusterUnst, err := client.Resource(v1alpha1ClusterGVR).Namespace(cluster.Namespace).Get(ctx, cluster.Name, metav1.GetOptions{})
						g.Expect(err).To(Succeed())
						fetched, err := typed.UnstructuredObjToTypedObj[*v1alpha1.SpiceDBCluster](clusterUnst)
						g.Expect(err).To(Succeed())
						logr.FromContextOrDiscard(ctx).Info("fetched cluster", "status", fetched.Status)
						g.Expect(fetched.Status.CurrentVersion).ToNot(BeNil())
						meta.RemoveStatusCondition(&fetched.Status.Conditions, v1alpha1.ConditionTypeConfigWarnings)
						g.Expect(len(fetched.Status.Conditions)).To(BeZero())
						g.Expect(fetched.Status.CurrentVersion.Name).To(Equal(to))
					}).Should(Succeed())

					AssertHealthySpiceDBCluster("", cluster.Name, Not(ContainSubstring("ERROR: kuberesolver")))
				})
			})
		}

		ValidateNewGraphEdges := func(engine string) {
			Describe("with a new update graph", Label("graph"), func() {
				proposedGraph := GetConfig(ProposedGraphFile)
				validatedGraph := GetConfig(ValidatedGraphFile)
				diffGraph := proposedGraph.UpdateGraph.Difference(&validatedGraph.UpdateGraph)
				for _, c := range diffGraph.Channels {
					if c.Metadata[updates.DatastoreMetadataKey] == engine {
						for source, targets := range c.Edges {
							source := source
							for _, target := range targets {
								target := target
								UpdateTest(engine, c.Name, source, target)
							}
						}
					}
				}
			})
		}

		Describe("With memory", Label("memory"), func() {
			BeforeEach(func() {
				db = &databases.LogicalDatabase{
					Engine: "memory",
				}
			})
			ValidateNewGraphEdges("memory")
		})

		Describe("With cockroachdb", Label("cockroachdb"), func() {
			BeforeEach(func() {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				db = crdbProvider.New(ctx)
				DeferCleanup(crdbProvider.Cleanup, context.Background(), db)
			})

			BasicSpiceDBFunctionality()
			ValidateNewGraphEdges("cockroachdb")
		})

		Describe("With mysql", Label("mysql"), func() {
			BeforeEach(func() {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				db = mysqlProvider.New(ctx)
				DeferCleanup(mysqlProvider.Cleanup, context.Background(), db)
			})

			BasicSpiceDBFunctionality()
			ValidateNewGraphEdges("mysql")
		})

		Describe("With postgres", Label("postgres"), func() {
			BeforeEach(func() {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				db = postgresProvider.New(ctx)
				DeferCleanup(postgresProvider.Cleanup, context.Background(), db)
			})

			BasicSpiceDBFunctionality()
			ValidateNewGraphEdges("postgres")

			Describe("there is a series of required migrations", Label("published"), func() {
				BeforeEach(func() {
					config = map[string]any{
						"skipReleaseCheck":             "true",
						"telemetryEndpoint":            "",
						"logLevel":                     "debug",
						"datastoreEngine":              "postgres",
						"tlsSecretName":                "spicedb-grpc-tls",
						"dispatchUpstreamCASecretName": "spicedb-grpc-tls",
						"dispatchUpstreamCAFilePath":   "ca.crt",
					}
					cluster.Spec.Version = "v1.13.0"

					secret.StringData = map[string]string{
						"datastore_uri":     db.DatastoreURI,
						"migration_secrets": "kaitain-bootstrap-token=testtesttesttest,sharewith-bootstrap-token=testtesttesttest,thumper-bootstrap-token=testtesttesttest,metrics-proxy-token=testtesttesttest",
						"preshared_key":     "testtesttesttest",
					}

					tlsSecret := GenerateCertManagerCompliantTLSSecretForService(
						ktypes.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace},
						ktypes.NamespacedName{Name: "spicedb-grpc-tls", Namespace: cluster.Namespace},
					)
					_, err := kclient.CoreV1().Secrets(cluster.Namespace).Create(ctx, tlsSecret, metav1.CreateOptions{})
					Expect(err).To(Succeed())
					DeferCleanup(kclient.CoreV1().Secrets(cluster.Namespace).Delete, ctx, tlsSecret.Name, metav1.DeleteOptions{})
				})

				JustBeforeEach(func() {
					AssertMigrationsCompleted("ghcr.io/authzed/spicedb:v1.13.0", "add-ns-config-id", "", cluster.Name, "postgres")

					Eventually(func(g Gomega) {
						clusterUnst, err := client.Resource(v1alpha1ClusterGVR).Namespace(cluster.Namespace).Get(ctx, cluster.Name, metav1.GetOptions{})
						g.Expect(err).To(Succeed())
						fetched, err := typed.UnstructuredObjToTypedObj[*v1alpha1.SpiceDBCluster](clusterUnst)
						g.Expect(err).To(Succeed())
						logr.FromContextOrDiscard(ctx).Info("fetched cluster", "status", fetched.Status)
						g.Expect(len(fetched.Status.Conditions)).To(BeZero())
						g.Expect(len(fetched.Status.AvailableVersions)).ToNot(BeZero(), "status should show available updates")
					}).Should(Succeed())

					// once the cluster is running at the initial version, update the target version
					Eventually(func(g Gomega) {
						clusterUnst, err := client.Resource(v1alpha1ClusterGVR).Namespace(cluster.Namespace).Get(ctx, cluster.Name, metav1.GetOptions{})
						g.Expect(err).To(Succeed())
						fetched, err := typed.UnstructuredObjToTypedObj[*v1alpha1.SpiceDBCluster](clusterUnst)
						g.Expect(err).To(Succeed())
						g.Expect(len(fetched.Status.Conditions)).To(BeZero())

						fetched.Spec.Version = "v1.14.1"
						u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(fetched)
						g.Expect(err).To(Succeed())
						_, err = client.Resource(v1alpha1ClusterGVR).Namespace(cluster.Namespace).Update(ctx, &unstructured.Unstructured{Object: u}, metav1.UpdateOptions{})
						g.Expect(err).To(Succeed())
					}).Should(Succeed())
				})

				AfterEach(func() {
					AssertDependentResourceCleanup(cluster.Name, "spicedb")
				})

				It("walks through a series of required migrations", func() {
					By("migrates to phase1")
					AssertMigrationsCompleted("ghcr.io/authzed/spicedb:v1.14.0", "add-xid-columns", "write-both-read-old", cluster.Name, "postgres")

					By("migrates to phase2")
					AssertMigrationsCompleted("ghcr.io/authzed/spicedb:v1.14.0", "add-xid-constraints", "write-both-read-new", cluster.Name, "postgres")

					By("migrates to phase3")
					AssertMigrationsCompleted("ghcr.io/authzed/spicedb:v1.14.0", "drop-bigserial-ids", "", cluster.Name, "postgres")

					By("updates to the next version")
					AssertHealthySpiceDBCluster("ghcr.io/authzed/spicedb:v1.14.1", cluster.Name, Not(ContainSubstring("ERROR: kuberesolver")))
				})
			})
		})

		Describe("With spanner", Label("spanner"), func() {
			BeforeEach(func() {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				// Each spanner test spins up its own database pod; the spanner
				// emulator doesn't support concurrent transactions so a single
				// instance can't be shared.
				spannerProvider := databases.NewSpannerProvider(mapper, restConfig, testNamespace)
				db = spannerProvider.New(ctx)
				DeferCleanup(spannerProvider.Cleanup, context.Background(), db)
			})

			BasicSpiceDBFunctionality()
			ValidateNewGraphEdges("spanner")
		})
	})
})
