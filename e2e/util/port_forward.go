package util

import (
	"net/http"

	//revive:disable:dot-imports convention is dot-import
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

var RestConfig *rest.Config

func PortForward(g Gomega, namespace, podName string, ports []string, stopChan <-chan struct{}) []portforward.ForwardedPort {
	GinkgoHelper()

	restConfig := rest.CopyConfig(RestConfig)
	transport, upgrader, err := spdy.RoundTripperFor(restConfig)
	if err != nil {
		GinkgoWriter.Println(err)
	}
	g.Expect(err).To(Succeed())
	readyc := make(chan struct{})
	restConfig.GroupVersion = &schema.GroupVersion{Group: "", Version: "v1"}
	if restConfig.APIPath == "" {
		restConfig.APIPath = "/api"
	}
	if restConfig.NegotiatedSerializer == nil {
		restConfig.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	}
	g.Expect(rest.SetKubernetesDefaults(restConfig)).To(Succeed())
	restClient, err := rest.RESTClientFor(restConfig)
	g.Expect(err).To(Succeed())

	req := restClient.Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("portforward")

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", req.URL())
	fw, err := portforward.New(dialer, ports, stopChan, readyc, GinkgoWriter, GinkgoWriter)
	if err != nil {
		GinkgoWriter.Println(err)
	}
	g.Expect(err).To(Succeed())
	go func() {
		defer GinkgoRecover()
		err := fw.ForwardPorts()
		if err != nil {
			GinkgoWriter.Println(err)
		}

		g.Expect(err).To(Succeed())
	}()
	for {
		select {
		case <-stopChan:
			GinkgoWriter.Println("port-forward canceled")
			return nil
		case <-readyc:
			localports, err := fw.GetPorts()
			if err != nil {
				GinkgoWriter.Println(err)
			}
			g.Expect(err).To(Succeed())
			return localports
		}
	}
}
