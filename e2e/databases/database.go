package databases

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/fluxcd/cli-utils/pkg/kstatus/polling"
	"github.com/fluxcd/pkg/ssa"
	//revive:disable:dot-imports convention is dot-import
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	crClient "sigs.k8s.io/controller-runtime/pkg/client"
)

//go:embed manifests/*.yaml
var datastores embed.FS

type LogicalDatabase struct {
	Engine       string
	DatastoreURI string
	DatabaseName string
	ExtraConfig  map[string]string
}

type Provider interface {
	New(ctx context.Context) *LogicalDatabase
	Cleanup(ctx context.Context, db *LogicalDatabase)
}

func CreateFromManifests(ctx context.Context, namespace, engine string, restConfig *rest.Config, mapper meta.RESTMapper) {
	ssaClient, err := crClient.NewWithWatch(restConfig, crClient.Options{
		Mapper: mapper,
	})
	Expect(err).To(Succeed())
	resourceManager := ssa.NewResourceManager(ssaClient, polling.NewStatusPoller(ssaClient, mapper, polling.Options{}), ssa.Owner{
		Field: "test.authzed.com",
		Group: "test.authzed.com",
	})

	yamlReader, err := datastores.Open(fmt.Sprintf("manifests/%s.yaml", engine))
	Expect(err).To(Succeed())
	DeferCleanup(yamlReader.Close)

	decoder := yaml.NewYAMLToJSONDecoder(yamlReader)
	objs := make([]*unstructured.Unstructured, 0)
	for {
		u := &unstructured.Unstructured{Object: map[string]any{}}
		err := decoder.Decode(&u.Object)
		if errors.Is(err, io.EOF) {
			break
		}
		Expect(err).To(Succeed())
		u.SetNamespace(namespace)
		objs = append(objs, u)
	}
	_, err = resourceManager.ApplyAll(ctx, objs, ssa.DefaultApplyOptions())
	Expect(err).To(Succeed())
	By(fmt.Sprintf("waiting for %s to start..", engine))
	stopWatching := watchForEarlyContainerFailures(ctx, namespace, restConfig)
	defer stopWatching()

	err = resourceManager.Wait(objs, ssa.WaitOptions{
		Interval: 1 * time.Second,
		Timeout:  120 * time.Second,
	})
	Expect(err).To(Succeed())
	By(fmt.Sprintf("%s running", engine))
}

// earlyFailurePollInterval has to be short relative to the kubelet's restart
// backoff, which starts around 10s: a container that dies on startup is only
// observable as its first instance for a few seconds.
const earlyFailurePollInterval = 2 * time.Second

// watchForEarlyContainerFailures reports the log of a container's *first*
// instance as soon as that container restarts, and returns a function that
// stops the watch.
//
// The first instance is usually the only one that shows the original cause.
// Later instances routinely fail for downstream reasons -- a half-written data
// directory, say -- and by the time a spec fails and the suite dumps state the
// container has restarted several times, leaving only the derived symptom.
// Kubernetes retains just the current and previous instance's logs, so the
// first one has to be captured while it is still the previous.
func watchForEarlyContainerFailures(ctx context.Context, namespace string, restConfig *rest.Config) func() {
	k, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		GinkgoWriter.Println("could not create client to watch for early container failures", err)
		return func() {}
	}

	pollCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		// This runs outside a spec goroutine, so an unrecovered panic here
		// would take down the whole suite with an unrelated-looking failure.
		defer GinkgoRecover()
		defer close(done)

		reported := map[string]bool{}
		ticker := time.NewTicker(earlyFailurePollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
			}

			pods, err := k.CoreV1().Pods(namespace).List(pollCtx, metav1.ListOptions{})
			if err != nil {
				continue
			}

			for _, pod := range pods.Items {
				statuses := append(append([]corev1.ContainerStatus{},
					pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...)

				for _, status := range statuses {
					key := pod.Name + "/" + status.Name
					if status.RestartCount == 0 || reported[key] {
						continue
					}
					reported[key] = true
					reportFirstInstanceLog(pollCtx, k, pod.Namespace, pod.Name, status)
				}
			}
		}
	}()

	return func() {
		cancel()
		<-done
	}
}

func reportFirstInstanceLog(ctx context.Context, k kubernetes.Interface, namespace, pod string, status corev1.ContainerStatus) {
	GinkgoWriter.Printf("container %s/%s restarted (restarts: %d); capturing the log of its first instance\n",
		namespace, pod+"/"+status.Name, status.RestartCount)

	if t := status.LastTerminationState.Terminated; t != nil {
		GinkgoWriter.Printf("  first instance: %s (exit %d), ran for %s\n",
			t.Reason, t.ExitCode, t.FinishedAt.Sub(t.StartedAt.Time))
	}

	logs, err := k.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{
		Container: status.Name,
		Previous:  true,
	}).DoRaw(ctx)
	if err != nil {
		GinkgoWriter.Printf("  could not read the first instance's log: %v\n", err)
		return
	}

	GinkgoWriter.Printf("  first instance log:\n%s\n", indentLines(string(logs), "    "))
}

func indentLines(s, indent string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, line := range lines {
		lines[i] = indent + line
	}
	return strings.Join(lines, "\n")
}
