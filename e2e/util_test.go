//go:build e2e

package e2e

import (
	"io"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/disk"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
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
	logger := logs.NewLogsOptions(genericclioptions.IOStreams{
		In:     os.Stdin,
		Out:    io.MultiWriter(writers...),
		ErrOut: io.MultiWriter(writers...),
	}, true)
	logger.Follow = true
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
	// TODO implement me
	panic("implement me")
}
