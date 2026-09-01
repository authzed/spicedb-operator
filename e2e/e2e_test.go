//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/gob"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/zapr"
	"github.com/jzelinskie/stringz"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	"github.com/spf13/afero"
	"go.uber.org/zap"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	applyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/klog/v2"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/utils/ptr"
	clientconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/tools/setup-envtest/env"
	"sigs.k8s.io/controller-runtime/tools/setup-envtest/remote"
	"sigs.k8s.io/controller-runtime/tools/setup-envtest/store"
	"sigs.k8s.io/controller-runtime/tools/setup-envtest/versions"
	"sigs.k8s.io/controller-runtime/tools/setup-envtest/workflows"
	kind "sigs.k8s.io/kind/pkg/cluster"
	"sigs.k8s.io/kind/pkg/cluster/nodeutils"
	"sigs.k8s.io/kind/pkg/cmd"
	"sigs.k8s.io/kind/pkg/fs"
	"sigs.k8s.io/yaml"

	"github.com/authzed/spicedb-operator/e2e/databases"
	e2eutil "github.com/authzed/spicedb-operator/e2e/util"
	"github.com/authzed/spicedb-operator/pkg/cmd/run"
	"github.com/authzed/spicedb-operator/pkg/config"
)

func listsep(c rune) bool {
	return c == ','
}

var (
	// - if run with `PROVISION=true` (the default), we spin up a new kind cluster for the tests
	// - if run with `APISERVER_ONLY=true`, we'll use apiserver + etcd instead of a real cluster
	// - if run with `PROVISION=false` and `APISERVER_ONLY=false` we'll use the environment / kubeconfig flags to connect to an existing cluster

	apiserverOnly      = os.Getenv("APISERVER_ONLY") == "true"
	provision          = os.Getenv("PROVISION") == "true"
	archives           = strings.FieldsFunc(os.Getenv("ARCHIVES"), listsep)
	images             = strings.FieldsFunc(os.Getenv("IMAGES"), listsep)
	ProposedGraphFile  = stringz.DefaultEmpty(os.Getenv("PROPOSED_GRAPH_FILE"), "../proposed-update-graph.yaml")
	ValidatedGraphFile = stringz.DefaultEmpty(os.Getenv("VALIDATED_GRAPH_FILE"), "../config/update-graph.yaml")
	graphExtraConfig   = strings.FieldsFunc(os.Getenv("GRAPH_EXTRA_CONFIG"), listsep)

	restConfig *rest.Config

	postgresProvider, mysqlProvider, crdbProvider databases.Provider
)

func init() {
	klog.InitFlags(nil)
	log.SetLogger(klog.NewKlogr())

	// Default operator logs to --v=4 and write to GinkgoWriter
	if verbosity := flag.CommandLine.Lookup("v"); verbosity.Value.String() == "" {
		Expect(verbosity.Value.Set("4")).To(Succeed())
	}
	klog.SetOutput(GinkgoWriter)

	RegisterFailHandler(SnapshotFailHandler)

	// Test Defaults
	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(1 * time.Second)
	SetDefaultConsistentlyDuration(5 * time.Second)
	SetDefaultConsistentlyPollingInterval(100 * time.Millisecond)
}

func TestEndToEnd(t *testing.T) {
	RunSpecs(t, "operator tests")
}

var testEnv *envtest.Environment

var _ = SynchronizedBeforeSuite(func() []byte {
	// this runs only once, no matter how many processes are running tests
	testEnv = &envtest.Environment{
		ControlPlaneStopTimeout: 3 * time.Minute,
	}

	if apiserverOnly {
		ConfigureApiserver()
	} else {
		ConfigureKube()
	}

	config, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(testEnv.Stop)

	run.DisableClientRateLimits(config)

	restConfig = config
	e2eutil.RestConfig = config

	seed := GinkgoRandomSeed()
	CreateNamespace(fmt.Sprintf("postgres-%d", seed))
	CreateNamespace(fmt.Sprintf("mysql-%d", seed))
	CreateNamespace(fmt.Sprintf("cockroachdb-%d", seed))

	StartOperator(config)
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	Expect(enc.Encode(config)).To(Succeed())
	return buf.Bytes()
}, func(rc []byte) {
	// this runs once per process, we grab the existing rest.Config here
	dec := gob.NewDecoder(bytes.NewReader(rc))
	var config rest.Config
	Expect(dec.Decode(&config)).To(Succeed())
	restConfig = &config
	e2eutil.RestConfig = &config
	dc, err := discovery.NewDiscoveryClientForConfig(restConfig)
	Expect(err).To(Succeed())
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))

	// Databases (except spanner) are spun up once per test suite run,
	// with parallel tests using different logical databases.
	// The kube resources are created lazily and won't deploy if no tests in
	// focus need that database.
	seed := GinkgoRandomSeed()
	postgresProvider = databases.NewPostgresProvider(mapper, restConfig, fmt.Sprintf("postgres-%d", seed))
	mysqlProvider = databases.NewMySQLProvider(mapper, restConfig, fmt.Sprintf("mysql-%d", seed))
	crdbProvider = databases.NewCockroachProvider(mapper, restConfig, fmt.Sprintf("cockroachdb-%d", seed))
})

var _ = SynchronizedAfterSuite(func() {}, func() {
	seed := GinkgoRandomSeed()
	DeleteNamespace(fmt.Sprintf("postgres-%d", seed))
	DeleteNamespace(fmt.Sprintf("mysql-%d", seed))
	DeleteNamespace(fmt.Sprintf("cockroachdb-%d", seed))
})

func CreateNamespace(name string) {
	kclient, err := kubernetes.NewForConfig(restConfig)
	Expect(err).To(Succeed())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err = kclient.CoreV1().Namespaces().Apply(ctx, applyv1.Namespace(name), metav1.ApplyOptions{FieldManager: "test"})
	Expect(err).To(Succeed())
}

func DeleteNamespace(name string) {
	kclient, err := kubernetes.NewForConfig(restConfig)
	Expect(err).To(Succeed())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	Expect(kclient.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})).To(Succeed())
}

func StartOperator(rc *rest.Config) {
	ctx, cancel := context.WithCancel(genericapiserver.SetupSignalContext())
	DeferCleanup(cancel)

	testRestConfig := rest.CopyConfig(restConfig)
	go func() {
		defer GinkgoRecover()
		options := run.RecommendedOptions()
		options.DebugAddress = "localhost:"
		options.BootstrapCRDs = true
		options.OperatorConfigPath = WriteConfig(GetConfig(ProposedGraphFile))
		_ = options.Run(ctx, cmdutil.NewFactory(ClientGetter{config: rc}))
	}()

	Eventually(func(g Gomega) {
		c, err := dynamic.NewForConfig(testRestConfig)
		g.Expect(err).To(Succeed())
		_, err = c.Resource(v1.SchemeGroupVersion.WithResource("customresourcedefinitions")).Get(ctx, v1alpha1ClusterGVR.GroupResource().String(), metav1.GetOptions{})
		g.Expect(err).To(Succeed())
	}).Should(Succeed())
}

var ConfigFileName = ""

func WriteConfig(operatorConfig config.OperatorConfig) string {
	out, err := yaml.Marshal(operatorConfig)
	Expect(err).To(Succeed())
	var file *os.File
	if len(ConfigFileName) == 0 {
		file, err = os.CreateTemp("", "operator-config")
		Expect(err).To(Succeed())
		ConfigFileName = file.Name()
	} else {
		file, err = os.OpenFile(ConfigFileName, os.O_WRONLY|os.O_TRUNC, os.ModeAppend)
		Expect(err).To(Succeed())
	}
	defer func() {
		Expect(file.Close()).To(Succeed())
	}()
	_, err = file.Write(out)
	Expect(err).To(Succeed())
	GinkgoWriter.Println("wrote new config to", ConfigFileName)

	return ConfigFileName
}

func GetConfig(fileName string) (cfg config.OperatorConfig) {
	file, err := os.Open(fileName)
	if err != nil {
		fmt.Println(err)
	}
	Expect(err).To(Succeed())
	defer file.Close()
	decoder := utilyaml.NewYAMLOrJSONDecoder(file, 100)
	err = decoder.Decode(&cfg)
	if err != nil {
		fmt.Println(err)
	}
	Expect(err).To(Succeed())
	return
}

func ConfigureApiserver() {
	logCfg := zap.NewDevelopmentConfig()
	logCfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	zapLog, err := logCfg.Build()
	Expect(err).To(Succeed())
	log := zapr.NewLogger(zapLog)

	e := &env.Env{
		Log: log,
		Client: &remote.HTTPClient{
			Log:      log,
			IndexURL: remote.DefaultIndexURL,
		},
		Version: versions.Spec{
			Selector:    versions.TildeSelector{},
			CheckLatest: false,
		},
		VerifySum:     true,
		ForceDownload: false,
		Platform: versions.PlatformItem{
			Platform: versions.Platform{
				OS:   runtime.GOOS,
				Arch: runtime.GOARCH,
			},
		},
		FS:    afero.Afero{Fs: afero.NewOsFs()},
		Store: store.NewAt("../testbin"),
		Out:   os.Stdout,
	}
	e.Version, err = versions.FromExpr("~1.32")
	Expect(err).To(Succeed())

	workflows.Use{
		UseEnv:      true,
		PrintFormat: env.PrintOverview,
		AssetsPath:  "../testbin",
	}.Do(e)

	Expect(os.Setenv("KUBEBUILDER_ASSETS", fmt.Sprintf("../testbin/k8s/%s-%s-%s", e.Version.AsConcrete(), e.Platform.OS, e.Platform.Arch))).To(Succeed())
	DeferCleanup(os.Unsetenv, "KUBEBUILDER_ASSETS")
}

func ConfigureKube() {
	var err error
	restConfig, err = clientconfig.GetConfig()

	// if no kubeconfig or explicitly told to provision, provision a cluster
	if err != nil || provision {
		// TODO: option to enable cleanup
		kubeconfigPath, _, err := Provision("spicedb-operator-e2e")
		Expect(err).To(Succeed())
		clientFactory := cmdutil.NewFactory(&genericclioptions.ConfigFlags{
			KubeConfig: &kubeconfigPath,
		})
		restConfig, err = clientFactory.ToRESTConfig()
		Expect(err).To(Succeed())
	}

	// if we have a connection to an existing cluster or started a new one,
	// we don't use envtest binaries (apiserver, etcd)
	if restConfig != nil {
		testEnv.UseExistingCluster = ptr.To(true)
		testEnv.Config = restConfig
	}
}

func Provision(name string) (string, func(), error) {
	provider := kind.NewProvider(
		kind.ProviderWithLogger(cmd.NewLogger()),
	)

	kubeconfig := fmt.Sprintf("%s.kubeconfig", name)

	var once sync.Once
	deprovision := func() {
		once.Do(func() {
			Expect(provider.Delete(name, kubeconfig)).To(Succeed())
			Expect(os.Remove(kubeconfig)).To(Succeed())
		})
	}

	var existing []string
	existing, err := provider.List()
	if err != nil {
		return kubeconfig, deprovision, err
	}

	needsCluster := true
	for _, c := range existing {
		if c == name {
			needsCluster = false
		}
	}
	if !needsCluster {
		err := provider.ExportKubeConfig(name, kubeconfig, false)
		return kubeconfig, deprovision, err
	}

	err = provider.Create(
		name,
		kind.CreateWithWaitForReady(5*time.Minute),
	)
	if err != nil {
		err = fmt.Errorf("failed to create kind controller: %w", err)
		return kubeconfig, deprovision, err
	}
	err = provider.ExportKubeConfig(name, kubeconfig, false)
	if err != nil {
		err = fmt.Errorf("failed to export kubeconfig: %w", err)
		return kubeconfig, deprovision, err
	}

	nodes, err := provider.ListNodes(name)
	if err != nil {
		return kubeconfig, deprovision, fmt.Errorf("failed to list kind nodes: %w", err)
	}

	if len(images) > 0 {
		dir, err := fs.TempDir("", "images-tar")
		if err != nil {
			return kubeconfig, deprovision, fmt.Errorf("failed to create tempdir for images: %w", err)
		}
		defer os.RemoveAll(dir)

		imagesTarPath := filepath.Join(dir, "images.tar")

		cmd := exec.Command("docker", append([]string{"save", "-o", imagesTarPath}, images...)...)
		session, err := gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		Eventually(session).Should(gexec.Exit(0))

		archives = append(archives, imagesTarPath)
	}

	if len(archives) > 0 {
		for _, archive := range archives {
			if archive == "" {
				continue
			}
			fmt.Printf("loading %s onto nodes\n", archive)
			for _, node := range nodes {
				fd, err := os.Open(archive)
				if err != nil {
					return kubeconfig, deprovision, fmt.Errorf("error opening archive %q: %w", archive, err)
				}
				err = nodeutils.LoadImageArchive(node, fd)
				if err != nil {
					return kubeconfig, deprovision, fmt.Errorf("error loading image archive %q to node %q: %w", archive, node, err)
				}
				if err := fd.Close(); err != nil {
					return kubeconfig, deprovision, fmt.Errorf("error loading image archive %q to node %q: %w", archive, node, err)
				}
			}
		}
	}

	return kubeconfig, deprovision, nil
}

// SnapshotFailHandler dumps cluster state when a test fails.
// It saves the resources below, plus container logs, from the per-spec "test"
// namespaces and the shared database namespaces.
func SnapshotFailHandler(message string, callerSkip ...int) {
	defer Fail(message, callerSkip...)

	gvrs := []schema.GroupVersionResource{
		v1alpha1ClusterGVR,
		corev1.SchemeGroupVersion.WithResource("pods"),
		batchv1.SchemeGroupVersion.WithResource("jobs"),
		corev1.SchemeGroupVersion.WithResource("secrets"),
		// Events explain pods that never start -- failed scheduling, image
		// pulls, or volumes that can't be provisioned -- and PVCs show whether
		// the requested storage was actually satisfied.
		corev1.SchemeGroupVersion.WithResource("events"),
		corev1.SchemeGroupVersion.WithResource("persistentvolumeclaims"),
	}

	SaveClusterState("./cluster-state/", gvrs)
}

// dumpNamespacePrefixes selects which namespaces are worth capturing on
// failure. The database namespaces matter as much as the per-spec ones: a
// database that never becomes ready fails specs in BeforeEach, and nothing in
// the test namespaces explains why.
var dumpNamespacePrefixes = []string{"test", "postgres-", "mysql-", "cockroachdb-"}

func shouldDumpNamespace(name string) bool {
	for _, prefix := range dumpNamespacePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// SaveClusterState saves the resources defined by `gvrs` to the specified directory.
func SaveClusterState(directory string, gvrs []schema.GroupVersionResource) {
	c, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		GinkgoWriter.Println("could not create client to report cluster state on error", err)
		return
	}

	k, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		GinkgoWriter.Println("could not create client to report cluster state on error", err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	namespaces, err := k.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		GinkgoWriter.Println("error fetching namespaces", err)
		// Return since we can make no further assertions here
		return
	}
	for _, n := range namespaces.Items {
		if !shouldDumpNamespace(n.Name) {
			continue
		}
		GinkgoWriter.Println("dumping namespace", n.Name)
		namespacePath := filepath.Join(directory, n.Name)
		err = os.MkdirAll(namespacePath, 0o700)
		if err != nil {
			GinkgoWriter.Println("error making directory for namespace", n.Name, err)
			continue
		}

		for _, gvr := range gvrs {
			GinkgoWriter.Println(n.Name, "dumping", gvr.Resource)
			spicedbPath := filepath.Join(namespacePath, gvr.Resource)
			err = os.MkdirAll(spicedbPath, 0o700)
			if err != nil {
				GinkgoWriter.Println("error making directory for", gvr.Resource, err)
				continue
			}
			objs, err := c.Resource(gvr).Namespace(n.Name).List(ctx, metav1.ListOptions{})
			if err != nil {
				GinkgoWriter.Println("error fetching", gvr.Resource, "from namespace", n.Name, err)
				continue
			}
			for _, item := range objs.Items {
				cluster, err := yaml.Marshal(item.Object)
				if err != nil {
					GinkgoWriter.Println("error fetching", gvr.Resource, item.GetName(), item.GetNamespace(), err)
					continue
				}
				err = os.WriteFile(filepath.Join(spicedbPath, item.GetName()+".yaml"), cluster, 0o700)
				if err != nil {
					GinkgoWriter.Println("error writing", gvr.Resource, item.GetName(), item.GetNamespace(), err)
					continue
				}
			}
		}

		savePodLogs(ctx, k, namespacePath, n.Name)
	}
}

// unreadyLogTailLines is how much of a failing container's log to inline in the
// test output: enough to carry a startup error, short enough not to bury the
// rest of the failure.
const unreadyLogTailLines = 40

// reportUnreadyContainers prints why a container isn't ready, and the tail of
// its log, to GinkgoWriter.
//
// The full logs are written to disk next to this, but those only reach someone
// who downloads the CI artifact. A container that never starts is usually the
// entire explanation for a failure, so it belongs in the test output itself
// where it shows up in the job log.
func reportUnreadyContainers(ctx context.Context, k kubernetes.Interface, pod *corev1.Pod) {
	statuses := make([]corev1.ContainerStatus, 0, len(pod.Status.InitContainerStatuses)+len(pod.Status.ContainerStatuses))
	statuses = append(statuses, pod.Status.InitContainerStatuses...)
	statuses = append(statuses, pod.Status.ContainerStatuses...)

	for _, status := range statuses {
		if status.Ready {
			continue
		}
		// A container that ran to completion is never Ready, but that is the
		// expected end state for Job pods (cockroach's cluster-init) rather
		// than something worth reporting.
		if t := status.State.Terminated; t != nil && t.ExitCode == 0 {
			continue
		}

		GinkgoWriter.Printf("pod %s/%s container %s NOT READY (restarts: %d)\n",
			pod.Namespace, pod.Name, status.Name, status.RestartCount)
		if w := status.State.Waiting; w != nil {
			GinkgoWriter.Printf("  waiting: %s: %s\n", w.Reason, w.Message)
		}
		if t := status.State.Terminated; t != nil {
			GinkgoWriter.Printf("  terminated: %s (exit %d): %s\n", t.Reason, t.ExitCode, t.Message)
		}
		if t := status.LastTerminationState.Terminated; t != nil {
			GinkgoWriter.Printf("  previously terminated: %s (exit %d): %s\n", t.Reason, t.ExitCode, t.Message)
		}

		// Prefer the current instance, but a container that has just been
		// restarted may not have written anything yet, so fall back to the
		// previous one.
		for _, previous := range []bool{false, true} {
			tail := int64(unreadyLogTailLines)
			logs, err := k.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
				Container: status.Name,
				Previous:  previous,
				TailLines: &tail,
			}).DoRaw(ctx)
			if err != nil || len(bytes.TrimSpace(logs)) == 0 {
				continue
			}

			label := "last " + strconv.Itoa(unreadyLogTailLines) + " log lines"
			if previous {
				label = "last " + strconv.Itoa(unreadyLogTailLines) + " log lines (previous instance)"
			}
			GinkgoWriter.Printf("  %s:\n%s\n", label, indentLines(string(logs), "    "))
			break
		}
	}
}

func indentLines(s, indent string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, line := range lines {
		lines[i] = indent + line
	}
	return strings.Join(lines, "\n")
}

// savePodLogs writes the container logs for every pod in a namespace. Object
// YAML shows that a container is running; only its logs show why a process
// inside it never started listening.
func savePodLogs(ctx context.Context, k kubernetes.Interface, namespacePath, namespace string) {
	pods, err := k.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		GinkgoWriter.Println("error listing pods for logs in namespace", namespace, err)
		return
	}

	logPath := filepath.Join(namespacePath, "logs")
	if err := os.MkdirAll(logPath, 0o700); err != nil {
		GinkgoWriter.Println("error making log directory for namespace", namespace, err)
		return
	}

	for _, pod := range pods.Items {
		reportUnreadyContainers(ctx, k, &pod)

		containers := make([]corev1.Container, 0, len(pod.Spec.InitContainers)+len(pod.Spec.Containers))
		containers = append(containers, pod.Spec.InitContainers...)
		containers = append(containers, pod.Spec.Containers...)

		for _, container := range containers {
			// The previous instance's logs are the useful ones for a container
			// that is crash-looping, since the current instance may have only
			// just started.
			for _, previous := range []bool{false, true} {
				name := pod.Name + "-" + container.Name + ".log"
				if previous {
					name = pod.Name + "-" + container.Name + "-previous.log"
				}

				logs, err := k.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
					Container: container.Name,
					Previous:  previous,
				}).DoRaw(ctx)
				if err != nil {
					// Having no previous instance is the normal case, so only
					// report a failure to read the current logs.
					if !previous {
						GinkgoWriter.Println("error fetching logs for", pod.Name, container.Name, err)
					}
					continue
				}

				if err := os.WriteFile(filepath.Join(logPath, name), logs, 0o700); err != nil {
					GinkgoWriter.Println("error writing logs for", pod.Name, container.Name, err)
				}
			}
		}
	}
}
