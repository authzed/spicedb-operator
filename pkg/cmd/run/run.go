package run

import (
	"context"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/errors"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/cli/globalflag"
	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/component-base/term"
	ctrlmanageropts "k8s.io/controller-manager/options"
	"k8s.io/klog/v2/textlogger"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"

	"github.com/authzed/controller-idioms/manager"
	ctrlmetrics "github.com/authzed/controller-idioms/metrics"
	"github.com/authzed/controller-idioms/static"
	"github.com/authzed/controller-idioms/typed"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/controller"
	"github.com/authzed/spicedb-operator/pkg/crds"
)

var v1alpha1ClusterGVR = v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.SpiceDBClusterResourceName)

// Options contains the input to the run command.
type Options struct {
	ConfigFlags  *genericclioptions.ConfigFlags
	DebugFlags   *ctrlmanageropts.DebuggingOptions
	DebugAddress string

	BootstrapCRDs         bool
	BootstrapSpicedbsPath string
	OperatorConfigPath    string

	MetricNamespace string

	WatchNamespaces []string
}

// RecommendedOptions builds a new options config with default values
func RecommendedOptions() *Options {
	return &Options{
		ConfigFlags:     genericclioptions.NewConfigFlags(true),
		DebugFlags:      ctrlmanageropts.RecommendedDebuggingOptions(),
		DebugAddress:    ":8080",
		MetricNamespace: "spicedb_operator",
	}
}

// NewCmdRun creates a command object for "run"
func NewCmdRun(o *Options) *cobra.Command {
	f := cmdutil.NewFactory(o.ConfigFlags)

	cmd := &cobra.Command{
		Use:                   "run [flags]",
		DisableFlagsInUseLine: true,
		Short:                 "run SpiceDB operator",
		Run: func(_ *cobra.Command, _ []string) {
			ctx := genericapiserver.SetupSignalContext()
			cmdutil.CheckErr(o.Validate())
			cmdutil.CheckErr(o.Run(ctx, f))
		},
	}

	namedFlagSets := &cliflag.NamedFlagSets{}
	bootstrapFlags := namedFlagSets.FlagSet("bootstrap")
	bootstrapFlags.BoolVar(&o.BootstrapCRDs, "crd", true, "if set, the operator will attempt to install/update the CRDs before starting up.")
	bootstrapFlags.StringVar(&o.BootstrapSpicedbsPath, "bootstrap-spicedbs", "", "set a path to a config file for spicedbs to load on start up.")
	debugFlags := namedFlagSets.FlagSet("debug")
	debugFlags.StringVar(&o.DebugAddress, "debug-address", o.DebugAddress, "address where debug information is served (/healthz, /metrics/, /debug/pprof, etc)")
	o.DebugFlags.AddFlags(debugFlags)
	kubernetesFlags := namedFlagSets.FlagSet("kubernetes")
	kubernetesFlags.StringSliceVar(&o.WatchNamespaces, "watch-namespaces", []string{}, "set a comma-separated list of namespaces to watch for CRDs.")
	o.ConfigFlags.AddFlags(kubernetesFlags)
	globalFlags := namedFlagSets.FlagSet("global")
	globalflag.AddGlobalFlags(globalFlags, cmd.Name())
	globalFlags.StringVar(&o.OperatorConfigPath, "config", "", "set a path to the operator's config file (configure registries, image tags, etc)")

	for _, f := range namedFlagSets.FlagSets {
		cmd.Flags().AddFlagSet(f)
	}

	cols, _, _ := term.TerminalSize(cmd.OutOrStdout())
	cliflag.SetUsageAndHelpFunc(cmd, *namedFlagSets, cols)

	return cmd
}

// Validate checks the set of flags provided by the user.
func (o *Options) Validate() error {
	return errors.NewAggregate(o.DebugFlags.Validate())
}

// Run performs the apply operation.
func (o *Options) Run(ctx context.Context, f cmdutil.Factory) error {
	restConfig, err := f.ToRESTConfig()
	if err != nil {
		return err
	}
	DisableClientRateLimits(restConfig)

	logger := textlogger.NewLogger(textlogger.NewConfig())

	dclient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return err
	}

	kclient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}

	if o.BootstrapCRDs {
		logger.V(3).Info("bootstrapping CRDs")
		if err := crds.BootstrapCRD(ctx, restConfig); err != nil {
			return err
		}
	}

	resources, err := f.OpenAPISchema()
	if err != nil {
		return err
	}

	registry := typed.NewRegistry()
	eventSink := &typedcorev1.EventSinkImpl{Interface: kclient.CoreV1().Events("")}
	broadcaster := record.NewBroadcaster()

	controllers := make([]manager.Controller, 0)
	if len(o.BootstrapSpicedbsPath) > 0 {
		staticSpiceDBController, err := static.NewStaticController[*v1alpha1.SpiceDBCluster](
			logger,
			"static-spicedbs",
			o.BootstrapSpicedbsPath,
			v1alpha1ClusterGVR,
			dclient)
		if err != nil {
			return err
		}
		controllers = append(controllers, staticSpiceDBController)
	}

	ctrl, err := controller.NewController(ctx, registry, dclient, kclient, resources, o.OperatorConfigPath, broadcaster, o.WatchNamespaces)
	if err != nil {
		return err
	}
	controllers = append(controllers, ctrl)

	// register with metrics collector
	spiceDBClusterMetrics := ctrlmetrics.NewConditionStatusCollector[*v1alpha1.SpiceDBCluster](o.MetricNamespace, "clusters", v1alpha1.SpiceDBClusterResourceName)

	if len(o.WatchNamespaces) == 0 {
		o.WatchNamespaces = []string{corev1.NamespaceAll}
	}
	for _, n := range o.WatchNamespaces {
		lister := typed.MustListerForKey[*v1alpha1.SpiceDBCluster](registry, typed.NewRegistryKey(controller.OwnedFactoryKey(n), v1alpha1ClusterGVR))
		spiceDBClusterMetrics.AddListerBuilder(func() ([]*v1alpha1.SpiceDBCluster, error) {
			return lister.List(labels.Everything())
		})
	}
	legacyregistry.CustomMustRegister(spiceDBClusterMetrics)

	if ctx.Err() != nil {
		return ctx.Err()
	}

	mgr := manager.NewManager(o.DebugFlags.DebuggingConfiguration, o.DebugAddress, broadcaster, eventSink)

	return mgr.Start(ctx, make(chan struct{}, 1), controllers...)
}

// DisableClientRateLimits removes rate limiting against the apiserver; we
// respect priority and fairness and will back off if the server tells us to
func DisableClientRateLimits(restConfig *rest.Config) {
	restConfig.Burst = 2000
	restConfig.QPS = -1
}
