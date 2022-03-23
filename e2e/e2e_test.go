package e2e

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/zapr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/afero"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	applyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/tools/setup-envtest/env"
	"sigs.k8s.io/controller-runtime/tools/setup-envtest/remote"
	"sigs.k8s.io/controller-runtime/tools/setup-envtest/store"
	"sigs.k8s.io/controller-runtime/tools/setup-envtest/versions"
	"sigs.k8s.io/controller-runtime/tools/setup-envtest/workflows"
	kind "sigs.k8s.io/kind/pkg/cluster"
	"sigs.k8s.io/kind/pkg/cluster/nodeutils"
	"sigs.k8s.io/kind/pkg/cmd"

	"github.com/authzed/spicedb-operator/pkg/cluster"
)

var (
	archives      = flag.String("archives", "", "list of archives to load into kind")
	provision     = flag.Bool("provision", true, "provision a kind cluster to run tests in")
	apiserverOnly = flag.Bool("apiserver-only", false, "run apiserver and etcd binaries instead of a kube cluster")

	restConfig *rest.Config
)

func TestEndToEnd(t *testing.T) {
	RegisterFailHandler(Fail)
	SetDefaultEventuallyTimeout(1 * time.Minute)
	SetDefaultEventuallyPollingInterval(100 * time.Millisecond)
	SetDefaultConsistentlyDuration(30 * time.Second)
	SetDefaultConsistentlyPollingInterval(100 * time.Millisecond)
	RunSpecs(t, "operator tests")
}

var testEnv *envtest.Environment

// - if run with `--provision=true` (the default), we spin up a new kind cluster for the tests
// - if run with `--apiserver-only=true`, we'll use apiserver + etcd instead of a real cluster
// - if run with `--provision=false` and `--apiserver-only=false` we'll use the environment / kubeconfig flags to connect to an existing cluster

var _ = BeforeSuite(func() {
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "config", "crds")},
		CRDInstallOptions: envtest.CRDInstallOptions{
			CleanUpAfterUse: false,
		},
		ControlPlaneStopTimeout: 3 * time.Minute,
	}

	if *apiserverOnly {
		ConfigureApiserver()
	} else {
		ConfigureKube()
	}

	var err error
	restConfig, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(testEnv.Stop)

	StartOperator()
	CreateNamespace("test")
	DeferCleanup(DeleteNamespace, "test")
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

func StartOperator() {
	dclient, err := dynamic.NewForConfig(restConfig)
	Expect(err).To(Succeed())

	kclient, err := kubernetes.NewForConfig(restConfig)
	Expect(err).To(Succeed())

	ctrl, err := cluster.NewController(context.Background(), dclient, kclient)
	Expect(err).To(Succeed())

	ctx, cancel := context.WithCancel(context.Background())
	DeferCleanup(cancel)
	go ctrl.Start(ctx, 2)
}

func ConfigureApiserver() {
	logCfg := zap.NewDevelopmentConfig()
	logCfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	zapLog, err := logCfg.Build()
	Expect(err).To(Succeed())
	log := zapr.NewLogger(zapLog)

	// no darwin arm builds yet
	// see: https://github.com/kubernetes-sigs/kubebuilder/pull/2516

	arch := runtime.GOARCH
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		arch = "amd64"
	}
	e := &env.Env{
		Log: log,
		Client: &remote.Client{
			Log:    log,
			Bucket: "kubebuilder-tools",
			Server: "storage.googleapis.com",
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
				Arch: arch,
			},
		},
		FS:    afero.Afero{Fs: afero.NewOsFs()},
		Store: store.NewAt("../testbin"),
		Out:   os.Stdout,
	}
	e.Version, err = versions.FromExpr("~1.22.1")
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
	restConfig, err = config.GetConfig()

	// if no kubeconfig or explictly told to provision, provision a cluster
	if err != nil || *provision == true {
		kubeconfigPath, cleanup, err := Provision()
		Expect(err).To(Succeed())
		DeferCleanup(cleanup)
		clientFactory := cmdutil.NewFactory(&genericclioptions.ConfigFlags{
			KubeConfig: &kubeconfigPath,
		})
		restConfig, err = clientFactory.ToRESTConfig()
		Expect(err).To(Succeed())
	}

	// if we have a connection to an existing cluster or started a new one,
	// we don't use envtest binaries (apiserver, etcd)
	if restConfig != nil {
		existingCluster := true
		testEnv.UseExistingCluster = &existingCluster
		testEnv.Config = restConfig
	}
}

func Provision() (string, func(), error) {
	provider := kind.NewProvider(
		kind.ProviderWithLogger(cmd.NewLogger()),
	)

	name := fmt.Sprintf("kind-%s", rand.String(16))
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
		return name, deprovision, nil
	}

	err = provider.Create(
		name,
		kind.CreateWithWaitForReady(5*time.Minute),
		kind.CreateWithKubeconfigPath(kubeconfig),
	)
	if err != nil {
		err = fmt.Errorf("failed to create kind cluster: %s", err.Error())
		return kubeconfig, deprovision, err
	}

	nodes, err := provider.ListNodes(name)
	if err != nil {
		return kubeconfig, deprovision, fmt.Errorf("failed to list kind nodes: %s", err.Error())
	}

	for _, archive := range strings.Split(*archives, ",") {
		if archive == "" {
			continue
		}
		fmt.Printf("loading %s onto nodes\n", archive)
		for _, node := range nodes {
			fd, err := os.Open(archive)
			if err != nil {
				return kubeconfig, deprovision, fmt.Errorf("error opening archive %q: %s", archive, err.Error())
			}
			err = nodeutils.LoadImageArchive(node, fd)
			if err != nil {
				return kubeconfig, deprovision, fmt.Errorf("error loading image archive %q to node %q: %s", archive, node, err.Error())
			}
			if err := fd.Close(); err != nil {
				return kubeconfig, deprovision, fmt.Errorf("error loading image archive %q to node %q: %s", archive, node, err.Error())
			}
		}
	}

	return kubeconfig, deprovision, nil
}
