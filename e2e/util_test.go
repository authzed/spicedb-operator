//go:build e2e

package e2e

import (
	"context"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/disk"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/cert"
	"k8s.io/kubectl/pkg/cmd/logs"
	"k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/kubectl/pkg/polymorphichelpers"
)

// TailF adds the logs from the object to the Ginkgo output
func TailF(obj runtime.Object) {
	Tail(obj, func(Gomega) {}, GinkgoWriter)
}

// Tail follows the logs from the object and passes them to a set of writers.
// This is useful to e.g. write to a buffer and assert properties of the logs.
func Tail(obj runtime.Object, assert func(g Gomega), writers ...io.Writer) {
	go func() {
		defer GinkgoRecover()
		Eventually(func(g Gomega) {
			logger := logs.NewLogsOptions(genericclioptions.IOStreams{
				In:     os.Stdin,
				Out:    io.MultiWriter(writers...),
				ErrOut: io.MultiWriter(writers...),
			})
			logger.Follow = false
			logger.IgnoreLogErrors = false
			logger.Object = obj
			logger.RESTClientGetter = util.NewFactory(ClientGetter{config: restConfig})
			logger.LogsForObject = polymorphichelpers.LogsForObjectFn
			logger.ConsumeRequestFn = logs.DefaultConsumeRequest
			var err error
			logger.Options, err = logger.ToLogOptions()
			g.Expect(err).To(Succeed())
			g.Expect(logger.Validate()).To(Succeed())
			g.Expect(logger.RunLogs()).To(Succeed())
			assert(g)
		}).Should(Succeed())
	}()
}

// Watch calls eventFn for every event starting at the given resourceVersion.
// eventFn returns false when the watch should stop
func Watch[K runtime.Object](ctx context.Context, client dynamic.Interface, gvr schema.GroupVersionResource, nn types.NamespacedName, rv string, eventFunc func(obj K) bool) {
	watcher, err := client.Resource(gvr).Namespace(nn.Namespace).Watch(ctx, metav1.ListOptions{
		Watch:           true,
		ResourceVersion: rv,
		FieldSelector:   fmt.Sprintf("metadata.name=%s", nn.Name),
	})
	Expect(err).To(Succeed())
	defer watcher.Stop()

	for event := range watcher.ResultChan() {
		var obj *K
		Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(event.Object.(*unstructured.Unstructured).Object, &obj)).To(Succeed())
		if !eventFunc(*obj) {
			break
		}
	}
}

// GenerateCertManagerCompliantTLSSecretForService creates a self-signed TLS
// keypair and returns a secret in the same format the cert-manager generates;
// with keys `tls.crt`, `tls.key`, and `ca.crt`.
func GenerateCertManagerCompliantTLSSecretForService(service, secret types.NamespacedName) *corev1.Secret {
	certPem, keyPem, err := cert.GenerateSelfSignedCertKey(service.Name, nil, []string{
		"localhost",
		service.Name + "." + service.Namespace,
		fmt.Sprintf("%s.%s.svc.cluster.local", service.Name, service.Namespace),
	})
	Expect(err).To(Succeed())
	_, rest := pem.Decode(certPem)
	ca, _ := pem.Decode(rest)

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secret.Name,
			Namespace: secret.Namespace,
		},
		Data: map[string][]byte{
			"tls.key": keyPem,
			"tls.crt": certPem,
			"ca.crt":  pem.EncodeToMemory(ca),
		},
	}
}

// ClientGetter implements RESTClientGetter to return values for the configured
// test.
type ClientGetter struct {
	config *rest.Config
}

var _ genericclioptions.RESTClientGetter = &ClientGetter{}

func (c ClientGetter) ToRESTConfig() (*rest.Config, error) {
	return c.config, nil
}

func (c ClientGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	cacheDir, err := os.MkdirTemp("", "kubecache")
	if err != nil {
		return nil, err
	}
	httpCacheDir := filepath.Join(cacheDir, "http")
	discoveryCacheDir := filepath.Join(cacheDir, "discovery")
	return disk.NewCachedDiscoveryClientForConfig(c.config, discoveryCacheDir, httpCacheDir, 5*time.Minute)
}

func (c ClientGetter) ToRESTMapper() (meta.RESTMapper, error) {
	dclient, err := c.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(dclient)
	expander := restmapper.NewShortcutExpander(mapper, dclient, func(s string) {
		GinkgoWriter.Println(s)
	})
	return expander, nil
}

func (c ClientGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	panic("test ClientGetter doesn't support raw kube config loading")
}
