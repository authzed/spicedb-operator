//go:build e2e

package e2e

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/disk"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"k8s.io/kubectl/pkg/cmd/logs"
	"k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/kubectl/pkg/polymorphichelpers"
)

func PortForward(namespace, podName string, ports []string, stopChan <-chan struct{}) {
	transport, upgrader, err := spdy.RoundTripperFor(restConfig)
	Expect(err).To(Succeed())
	readyc := make(chan struct{})
	restConfig.GroupVersion = &schema.GroupVersion{Group: "", Version: "v1"}
	if restConfig.APIPath == "" {
		restConfig.APIPath = "/api"
	}
	if restConfig.NegotiatedSerializer == nil {
		restConfig.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	}
	Expect(rest.SetKubernetesDefaults(restConfig)).To(Succeed())
	restClient, err := rest.RESTClientFor(restConfig)
	Expect(err).To(Succeed())

	req := restClient.Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("portforward")

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", req.URL())
	fw, err := portforward.New(dialer, ports, stopChan, readyc, GinkgoWriter, GinkgoWriter)
	Expect(err).To(Succeed())
	go func() {
		Expect(fw.ForwardPorts()).To(Succeed())
	}()
	<-readyc
}

// TailF adds the logs from the object to the Ginkgo output
func TailF(obj runtime.Object) {
	Tail(obj, func(Gomega) {}, GinkgoWriter)
}

// Tail follows the logs from the object and passes them to a set of writers.
// This is useful to e.g. write to a buffer and assert properties of the logs.
func Tail(obj runtime.Object, assert func(g Gomega), writers ...io.Writer) {
	logger := logs.NewLogsOptions(genericclioptions.IOStreams{
		In:     os.Stdin,
		Out:    io.MultiWriter(writers...),
		ErrOut: io.MultiWriter(writers...),
	}, true)
	logger.Follow = true
	logger.IgnoreLogErrors = false
	logger.Object = obj
	logger.RESTClientGetter = util.NewFactory(ClientGetter{})
	logger.LogsForObject = polymorphichelpers.LogsForObjectFn
	logger.ConsumeRequestFn = logs.DefaultConsumeRequest
	var err error
	logger.Options, err = logger.ToLogOptions()
	Expect(err).To(Succeed())
	Expect(logger.Validate()).To(Succeed())
	go func() {
		defer GinkgoRecover()
		Eventually(func(g Gomega) {
			g.Expect(logger.RunLogs()).To(Succeed())
			assert(g)
		}).Should(Succeed())
	}()
}

// ClientGetter implements RESTClientGetter to return values for the configured
// test.
type ClientGetter struct{}

var _ genericclioptions.RESTClientGetter = &ClientGetter{}

func (c ClientGetter) ToRESTConfig() (*rest.Config, error) {
	return restConfig, nil
}

func (c ClientGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	cacheDir, err := os.MkdirTemp("", "kubecache")
	if err != nil {
		return nil, err
	}
	httpCacheDir := filepath.Join(cacheDir, "http")
	discoveryCacheDir := filepath.Join(cacheDir, "discovery")
	return disk.NewCachedDiscoveryClientForConfig(restConfig, discoveryCacheDir, httpCacheDir, 5*time.Minute)
}

func (c ClientGetter) ToRESTMapper() (meta.RESTMapper, error) {
	dclient, err := c.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(dclient)
	expander := restmapper.NewShortcutExpander(mapper, dclient)
	return expander, nil
}

func (c ClientGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	panic("test ClientGetter doesn't support raw kube config loading")
}
