package bootstrap

import (
	"bufio"
	"context"
	"errors"
	"io"
	"io/ioutil"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

// Cluster bootstraps a cluster with the given config file
func Cluster(ctx context.Context, dclient dynamic.Interface, configPath string) error {
	if len(configPath) <= 0 {
		klog.V(4).Info("bootstrap file path not specified")
		return nil
	}

	f, err := os.Open(configPath)
	defer f.Close()
	if errors.Is(err, os.ErrNotExist) {
		klog.V(4).Info("no bootstrap file present, skipping bootstrapping")
		return nil
	}
	if err != nil {
		return err
	}

	decoder := yaml.NewYAMLToJSONDecoder(ioutil.NopCloser(bufio.NewReader(f)))
	for {
		var clusterSpec v1alpha1.SpiceDBCluster
		if err := decoder.Decode(&clusterSpec); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}

		data, err := client.Apply.Data(&clusterSpec)
		if err != nil {
			return err
		}

		v1alpha1ClusterGVR := v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.SpiceDBClusterResourceName)
		_, err = dclient.
			Resource(v1alpha1ClusterGVR).
			Namespace(clusterSpec.Namespace).
			Patch(ctx, clusterSpec.Name, types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: "spicedb-operator"})
		if err != nil {
			return err
		}
	}

	return nil
}
