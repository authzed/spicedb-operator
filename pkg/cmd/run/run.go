package run

import (
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/util/errors"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/cli/globalflag"
	"k8s.io/component-base/term"
	ctrlmanageropts "k8s.io/controller-manager/options"
	"k8s.io/klog/v2"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/controller"
	"github.com/authzed/spicedb-operator/pkg/crds"
	"github.com/authzed/spicedb-operator/pkg/libctrl/manager"
	"github.com/authzed/spicedb-operator/pkg/libctrl/static"
	"github.com/authzed/spicedb-operator/pkg/libctrl/typed"
)

// Options contains the input to the run command.
type Options struct {
	ConfigFlags  *genericclioptions.ConfigFlags
	DebugFlags   *ctrlmanageropts.DebuggingOptions
	DebugAddress string

	BootstrapCRDs         bool
	BootstrapSpicedbsPath string
	OperatorConfigPath    string
}

// RecommendedOptions builds a new options config with default values
func RecommendedOptions() *Options {
	return &Options{
		ConfigFlags:  genericclioptions.NewConfigFlags(true),
		DebugFlags:   ctrlmanageropts.RecommendedDebuggingOptions(),
		DebugAddress: ":8080",
	}
}

// NewCmdRun creates a command object for "run"
func NewCmdRun(o *Options) *cobra.Command {
	f := cmdutil.NewFactory(o.ConfigFlags)

	cmd := &cobra.Command{
		Use:                   "run [flags]",
		DisableFlagsInUseLine: true,
		Short:                 "run SpiceDB operator",
		Run: func(cmd *cobra.Command, args []string) {
			cmdutil.CheckErr(o.Validate(cmd, args))
			cmdutil.CheckErr(o.Run(f, cmd, args))
		},
	}

	namedFlagSets := &cliflag.NamedFlagSets{}
	bootstrapFlags := namedFlagSets.FlagSet("bootstrap")
	bootstrapFlags.BoolVar(&o.BootstrapCRDs, "crd", true, "if set, the operator will attempt to install/update the CRDs before starting up.")
	bootstrapFlags.StringVar(&o.BootstrapSpicedbsPath, "bootstrap-spicedbs", "", "set a path to a config file for spicedbs to load on start up.")
	debugFlags := namedFlagSets.FlagSet("debug")
	debugFlags.StringVar(&o.DebugAddress, "debug-address", o.DebugAddress, "address where debug information is served (/healthz, /metrics/, /debug/pprof, etc)")
	o.ConfigFlags.AddFlags(namedFlagSets.FlagSet("kubernetes"))
	o.DebugFlags.AddFlags(debugFlags)
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
func (o *Options) Validate(cmd *cobra.Command, args []string) error {
	return errors.NewAggregate(o.DebugFlags.Validate())
}

// Run performs the apply operation.
func (o *Options) Run(f cmdutil.Factory, cmd *cobra.Command, args []string) error {
	restConfig, err := f.ToRESTConfig()
	if err != nil {
		return err
	}

	// remove rate limiting against the apiserver; we respect priority and fairness
	// and will back off if the server tells us to
	restConfig.Burst = 2000
	restConfig.QPS = -1

	dclient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return err
	}

	kclient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}

	if o.BootstrapCRDs {
		klog.V(3).InfoS("bootstrapping CRDs")
		if err := crds.BootstrapCRD(restConfig); err != nil {
			return err
		}
	}

	ctx := genericapiserver.SetupSignalContext()
	registry := typed.NewRegistry()

	controllers := make([]manager.Controller, 0)
	if len(o.BootstrapSpicedbsPath) > 0 {
		staticSpiceDBController, err := static.NewStaticController[*v1alpha1.SpiceDBCluster](
			"static-spicedbs",
			o.BootstrapSpicedbsPath,
			v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.SpiceDBClusterResourceName),
			dclient)
		if err != nil {
			return err
		}
		controllers = append(controllers, staticSpiceDBController)
	}

	ctrl, err := controller.NewController(ctx, registry, dclient, kclient, o.OperatorConfigPath)
	if err != nil {
		return err
	}
	controllers = append(controllers, ctrl)

	if ctx.Err() != nil {
		return ctx.Err()
	}
	mgr := manager.NewManager(o.DebugFlags.DebuggingConfiguration, o.DebugAddress)

	return mgr.Start(ctx, controllers...)
}
