package run

import (
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/util/errors"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/cli/globalflag"
	"k8s.io/component-base/term"
	ctrlmanageropts "k8s.io/controller-manager/options"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/authzed/spicedb-operator/pkg/cluster"
	"github.com/authzed/spicedb-operator/pkg/manager"
)

// Options contains the input to the run command.
type Options struct {
	ConfigFlags  *genericclioptions.ConfigFlags
	DebugFlags   *ctrlmanageropts.DebuggingOptions
	DebugAddress string

	CRDPaths []string
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
		Short:                 "run Authzed Stack operator",
		Run: func(cmd *cobra.Command, args []string) {
			cmdutil.CheckErr(o.Validate(cmd, args))
			cmdutil.CheckErr(o.Run(f, cmd, args))
		},
	}

	namedFlagSets := &cliflag.NamedFlagSets{}
	bootstrapFlags := namedFlagSets.FlagSet("bootstrap")
	bootstrapFlags.StringArrayVar(&o.CRDPaths, "crd", []string{}, "if set, the operator will attempt to install/update the CRDs found at the specified path before starting up.")
	debugFlags := namedFlagSets.FlagSet("debug")
	debugFlags.StringVar(&o.DebugAddress, "debug-address", o.DebugAddress, "address where debug information is served (/healthz, /metrics/, /debug/pprof, etc)")
	o.ConfigFlags.AddFlags(namedFlagSets.FlagSet("kubernetes"))
	o.DebugFlags.AddFlags(debugFlags)
	globalflag.AddGlobalFlags(namedFlagSets.FlagSet("global"), cmd.Name())

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

	ctx := genericapiserver.SetupSignalContext()
	ctrl, err := cluster.NewController(ctx, dclient, kclient)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	mgr := manager.NewManager(o.DebugFlags.DebuggingConfiguration, o.DebugAddress)

	if len(o.CRDPaths) > 0 {
		restConfig, err := f.ToRESTConfig()
		if err != nil {
			return err
		}
		if err := BootstrapCRD(o.CRDPaths, restConfig); err != nil {
			return err
		}
	}

	return mgr.StartControllers(ctx, ctrl)
}

func BootstrapCRD(crdPaths []string, restConfig *rest.Config) error {
	_, err := envtest.InstallCRDs(restConfig, envtest.CRDInstallOptions{
		Paths:              crdPaths,
		ErrorIfPathMissing: true,
		MaxTime:            5 * time.Minute,
		PollInterval:       200 * time.Millisecond,
		CleanUpAfterUse:    false,
	})
	return err
}
