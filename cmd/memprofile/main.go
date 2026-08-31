// Command memprofile attributes the spicedb-operator's startup memory to the
// phases of its startup path, so that OOMKills at a given container limit can
// be traced to a specific allocation rather than guessed at.
//
// It deliberately does not start the operator. Instead it walks the same
// sequence pkg/cmd/run.Options.Run walks, taking a memory snapshot between each
// step, so that each step's retained heap is reported separately:
//
//	baseline    process start: runtime, scheme registration, client construction
//	discovery   REST config + discovery/dynamic/typed clients
//	controller  controller.NewController() -- informers, handler chain, caches
//	openapi     the group-versions the operator patches, resolved via OpenAPI v3
//
// The operator resolves no schema until a SpiceDBCluster uses a strategic merge
// patch, so --stage=controller (the default) reflects a normal startup, and
// --stage=openapi measures what a patch-using cluster additionally pays.
//
// The number to compare against the container's memory limit is Max RSS, which
// is the kernel high-water mark the OOM killer actually acts on. HeapAlloc
// after a forced GC is the *retained* cost of a phase; TotalAlloc delta is the
// churn through it (which drives GC pressure and the transient peak).
//
// Usage:
//
//	go run ./cmd/memprofile                       # normal startup path
//	go run ./cmd/memprofile --stage=openapi       # the schema cost when patching
//	go run ./cmd/memprofile --stage=all           # all phases
//	go run ./cmd/memprofile --profile-dir=/tmp/p  # per-phase heap profiles
//
// Then, to see what is holding the retained bytes:
//
//	go tool pprof -http=: /tmp/p/openapi.heap
package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/openapi3"
	"k8s.io/client-go/tools/record"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"

	"github.com/authzed/controller-idioms/typed"

	"github.com/authzed/spicedb-operator/pkg/cmd/run"
	"github.com/authzed/spicedb-operator/pkg/config"
	"github.com/authzed/spicedb-operator/pkg/controller"
)

// Every stage builds on `discovery`, since the clients are needed throughout.
// `controller` is the operator's real startup path and deliberately resolves no
// schema at all, because the operator defers that until a cluster uses a
// strategic merge patch. `openapi` forces the resolution of every group-version
// the operator patches, so that deferred cost is measurable. `all` runs
// controller first, so its RSS is read before the openapi phase inflates it.
const (
	stageDiscovery  = "discovery"
	stageOpenAPI    = "openapi"
	stageController = "controller"
	stageAll        = "all"
)

var stageOrder = []string{stageDiscovery, stageOpenAPI, stageController, stageAll}

// patchedKinds names one kind per group-version the operator patches, so the v3
// stage fetches exactly the group-versions a real patch would.
var patchedKinds = []schema.GroupVersionKind{
	{Group: "", Version: "v1", Kind: "Service"},
	{Group: "apps", Version: "v1", Kind: "Deployment"},
	{Group: "batch", Version: "v1", Kind: "Job"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "Role"},
	{Group: "policy", Version: "v1", Kind: "PodDisruptionBudget"},
}

type options struct {
	stage            string
	profileDir       string
	cacheSyncWait    time.Duration
	reportAPISurface bool
}

func main() {
	o := &options{}
	runOpts := run.RecommendedOptions()

	cmd := &cobra.Command{
		Use:          "memprofile [flags]",
		Short:        "attribute spicedb-operator startup memory to startup phases",
		SilenceUsage: true,
		Long: strings.TrimSpace(`
Measures where the operator's startup memory goes, phase by phase, against the
cluster in the active kubecontext. Reports Max RSS (what the OOM killer sees),
retained heap per phase, and allocation churn per phase.`),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return o.run(cmd.Context(), runOpts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&o.stage, "stage", stageController,
		fmt.Sprintf("which startup phases to measure, one of %s", strings.Join(stageOrder, "|")))
	flags.StringVar(&o.profileDir, "profile-dir", "",
		"if set, write a heap profile per phase into this directory for `go tool pprof`")
	flags.DurationVar(&o.cacheSyncWait, "cache-sync-timeout", 90*time.Second,
		"how long the controller phase waits for informer caches to sync before giving up")
	flags.BoolVar(&o.reportAPISurface, "api-surface", true,
		"report the cluster's API surface (group/resource/CRD counts), which is what the openapi phase's cost scales with")

	// Reuse the operator's own kubeconfig flags so that --kubeconfig, --context
	// and friends behave identically to `spicedb-operator run`.
	runOpts.ConfigFlags.AddFlags(flags)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := cmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func (o *options) run(ctx context.Context, runOpts *run.Options) error {
	stageIdx := indexOf(stageOrder, o.stage)
	if stageIdx < 0 {
		return fmt.Errorf("unknown --stage %q, want one of %s", o.stage, strings.Join(stageOrder, "|"))
	}
	if o.profileDir != "" {
		if err := os.MkdirAll(o.profileDir, 0o755); err != nil {
			return err
		}
	}

	// The operator sets no GOMEMLIMIT, so report what the runtime believes its
	// ceiling is. A ceiling of ~9.2EB means "unset", i.e. the runtime will size
	// the heap at GOGC% above live heap with no regard for the cgroup limit.
	fmt.Printf("go %s  GOMAXPROCS=%d  GOGC=%d  GOMEMLIMIT=%s\n\n",
		runtime.Version(), runtime.GOMAXPROCS(0), currentGOGC(), formatMemLimit(debug.SetMemoryLimit(-1)))

	r := &reporter{profileDir: o.profileDir}
	r.snap("baseline")

	// --- discovery: REST config and the clients run.Options.Run builds ---
	f := cmdutil.NewFactory(runOpts.ConfigFlags)
	restConfig, err := f.ToRESTConfig()
	if err != nil {
		return fmt.Errorf("building REST config (is a kubecontext active?): %w", err)
	}
	run.DisableClientRateLimits(restConfig)

	dclient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return err
	}
	kclient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		return err
	}
	fmt.Printf("cluster: %s\n", restConfig.Host)
	r.snap(stageDiscovery)

	var apiSurface string
	if o.reportAPISurface {
		apiSurface, err = describeAPISurface(discoveryClient)
		if err != nil {
			// A partial discovery failure is normal on clusters with a broken
			// aggregated APIService; it should not abort the measurement.
			apiSurface = fmt.Sprintf("(partial: %v)", err)
		}
	}

	if o.stage == stageDiscovery {
		r.report(apiSurface)
		return nil
	}

	openAPIV3Client, err := f.OpenAPIV3Client()
	if err != nil {
		return err
	}

	// --- controller: the operator's real startup path ---
	// A v3 resolver is handed over exactly as pkg/cmd/run does, so nothing here
	// resolves any schema.
	if o.stage == stageController || o.stage == stageAll {
		syncCtx, cancel := context.WithTimeout(ctx, o.cacheSyncWait)
		defer cancel()

		ctrl, err := controller.NewController(
			syncCtx,
			typed.NewRegistry(),
			dclient,
			kclient,
			config.NewV3PatchMetaResolver(openapi3.NewRoot(openAPIV3Client)),
			"", // no operator config file; the update graph is measured separately
			"",
			record.NewBroadcaster(),
			nil, // all namespaces, matching the operator's default
		)
		if err != nil {
			return fmt.Errorf("building controller: %w", err)
		}
		if syncCtx.Err() != nil {
			fmt.Fprintf(os.Stderr,
				"warning: informer caches did not sync within %s; the controller phase is understated\n", o.cacheSyncWait)
		}
		r.retain(ctrl)
		r.snap(stageController)
	}

	// --- openapi: the schema cost a patch-using cluster pays ---
	if o.stage == stageOpenAPI || o.stage == stageAll {
		resolver := config.NewV3PatchMetaResolver(openapi3.NewRoot(openAPIV3Client))
		for _, gvk := range patchedKinds {
			if _, err := resolver.LookupPatchMeta(gvk); err != nil {
				// A cluster may legitimately lack a group-version (policy/v1 on
				// very old servers); report and keep going so the rest is still
				// measured.
				fmt.Fprintf(os.Stderr, "warning: couldn't resolve %s: %v\n", gvk, err)
			}
		}
		// Retained exactly as Config.PatchMeta retains it for the process life.
		r.retain(resolver)
		r.snap(stageOpenAPI)
	}

	r.report(apiSurface)
	return nil
}

// reporter accumulates per-phase memory snapshots and keeps every measured
// object alive until the final report, so that retained heap is not optimized
// or collected away mid-run.
type reporter struct {
	profileDir string
	snaps      []snapshot
	retained   []any
}

type snapshot struct {
	label      string
	heapAlloc  uint64
	heapSys    uint64
	sys        uint64
	totalAlloc uint64
	maxRSS     uint64
}

func (r *reporter) retain(v any) { r.retained = append(r.retained, v) }

func (r *reporter) snap(label string) {
	// Two collections: the first frees unreachable objects, the second reclaims
	// anything whose finalizer the first pass queued, so HeapAlloc settles on
	// genuinely-retained bytes.
	runtime.GC()
	runtime.GC()

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)

	r.snaps = append(r.snaps, snapshot{
		label:      label,
		heapAlloc:  ms.HeapAlloc,
		heapSys:    ms.HeapSys,
		sys:        ms.Sys,
		totalAlloc: ms.TotalAlloc,
		maxRSS:     maxRSSBytes(ru),
	})

	if r.profileDir != "" {
		path := filepath.Join(r.profileDir, label+".heap")
		f, err := os.Create(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not write heap profile %s: %v\n", path, err)
			return
		}
		defer f.Close()
		if err := pprof.WriteHeapProfile(f); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not write heap profile %s: %v\n", path, err)
		}
	}
}

func (r *reporter) report(apiSurface string) {
	if apiSurface != "" {
		fmt.Printf("\napi surface: %s\n", apiSurface)
	}

	fmt.Printf("\n%-12s %12s %12s %12s %12s\n",
		"phase", "retained", "+retained", "churn", "max rss")
	fmt.Println(strings.Repeat("-", 64))
	for i, s := range r.snaps {
		var retainedDelta, churn string
		if i == 0 {
			retainedDelta, churn = "-", "-"
		} else {
			prev := r.snaps[i-1]
			retainedDelta = "+" + humanBytes(saturatingSub(s.heapAlloc, prev.heapAlloc))
			churn = humanBytes(saturatingSub(s.totalAlloc, prev.totalAlloc))
		}
		fmt.Printf("%-12s %12s %12s %12s %12s\n",
			s.label, humanBytes(s.heapAlloc), retainedDelta, churn, humanBytes(s.maxRSS))
	}

	last := r.snaps[len(r.snaps)-1]
	fmt.Printf("\nretained live heap: %s\n", humanBytes(last.heapAlloc))
	fmt.Printf("heap reserved from OS (HeapSys): %s\n", humanBytes(last.heapSys))
	fmt.Printf("total runtime footprint (Sys): %s\n", humanBytes(last.sys))
	fmt.Printf("MAX RSS (compare this to the container limit): %s\n", humanBytes(last.maxRSS))

	// Keep everything alive past the final read.
	runtime.KeepAlive(r.retained)
}

// describeAPISurface counts the cluster's API surface. It is reported for
// context: a whole-cluster schema would scale with these numbers, whereas the
// per-group-version resolution the operator uses does not.
func describeAPISurface(d discovery.DiscoveryInterface) (string, error) {
	lists, err := d.ServerPreferredResources()
	// ServerPreferredResources returns partial results alongside an error when
	// some group is unreachable; count whatever came back either way.
	groups := map[string]struct{}{}
	resourceCount := 0
	for _, l := range lists {
		if l == nil {
			continue
		}
		groups[l.GroupVersion] = struct{}{}
		resourceCount += len(l.APIResources)
	}
	summary := fmt.Sprintf("%d group-versions, %d resources", len(groups), resourceCount)
	if err != nil {
		return summary, err
	}
	return summary, nil
}

func currentGOGC() int {
	// SetGCPercent returns the previous value; set it straight back.
	prev := debug.SetGCPercent(100)
	debug.SetGCPercent(prev)
	return prev
}

func formatMemLimit(limit int64) string {
	// math.MaxInt64 is the runtime's "no limit" sentinel. A negative limit is
	// not reachable via SetMemoryLimit, but guard the conversion regardless.
	if limit == math.MaxInt64 || limit < 0 {
		return "unset (no ceiling; heap grows to GOGC% above live heap)"
	}
	return humanBytes(uint64(limit))
}

func maxRSSBytes(ru syscall.Rusage) uint64 {
	if ru.Maxrss < 0 {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return uint64(ru.Maxrss)
	}
	return uint64(ru.Maxrss) * 1024 // linux reports kilobytes
}

func saturatingSub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	suffixes := []string{"KiB", "MiB", "GiB", "TiB"}
	val := float64(b)
	i := -1
	for val >= unit && i < len(suffixes)-1 {
		val /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", val, suffixes[i])
}

func indexOf(haystack []string, needle string) int {
	return slices.Index(haystack, needle)
}
