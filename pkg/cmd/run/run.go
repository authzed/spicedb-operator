package run

import (
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/util/errors"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/cli/globalflag"
	"k8s.io/component-base/term"
	ctrlmanageropts "k8s.io/controller-manager/options"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"

	"github.com/authzed/spicedb-operator/pkg/bootstrap"
	"github.com/authzed/spicedb-operator/pkg/controller"
	"github.com/authzed/spicedb-operator/pkg/libctrl/manager"
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
	dclient, err := f.DynamicClient()
	if err != nil {
		return err
	}

	kclient, err := f.KubernetesClientSet()
	if err != nil {
		return err
	}

	if o.BootstrapCRDs {
		restConfig, err := f.ToRESTConfig()
		if err != nil {
			return err
		}
		if err := bootstrap.CRD(restConfig); err != nil {
			return err
		}
	}

	ctx := genericapiserver.SetupSignalContext()
	ctrl, err := controller.NewController(ctx, dclient, kclient, o.OperatorConfigPath, o.BootstrapSpicedbsPath)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	mgr := manager.NewManager(o.DebugFlags.DebuggingConfiguration, o.DebugAddress)

	return mgr.StartControllers(ctx, ctrl)
}
