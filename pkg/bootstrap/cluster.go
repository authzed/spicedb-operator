package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/ioutil"
	"os"

	"github.com/cespare/xxhash/v2"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

// Cluster bootstraps a cluster with the given config file
func Cluster(ctx context.Context, dclient dynamic.Interface, configPath string, lastHash uint64) (uint64, error) {
	if len(configPath) <= 0 {
		klog.V(4).Info("bootstrap file path not specified")
		return 0, nil
	}

	f, err := os.Open(configPath)
	defer func() {
		utilruntime.HandleError(f.Close())
	}()
	if errors.Is(err, os.ErrNotExist) {
		klog.V(4).Info("no bootstrap file present, skipping bootstrapping")
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	contents, err := ioutil.ReadAll(f)
	if err != nil {
		return 0, err
	}
	hash := xxhash.Sum64(contents)

	// no changes since last apply
	if lastHash == hash {
		return hash, nil
	}

	decoder := yaml.NewYAMLToJSONDecoder(bytes.NewReader(contents))
	for {
		var clusterSpec v1alpha1.SpiceDBCluster
		if err := decoder.Decode(&clusterSpec); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return hash, err
		}

		data, err := client.Apply.Data(&clusterSpec)
		if err != nil {
			return hash, err
		}

		v1alpha1ClusterGVR := v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.SpiceDBClusterResourceName)
		_, err = dclient.
			Resource(v1alpha1ClusterGVR).
			Namespace(clusterSpec.Namespace).
			Patch(ctx, clusterSpec.Name, types.ApplyPatchType, data, metadata.PatchForceOwned)
		if err != nil {
			return hash, err
		}
	}

	return hash, nil
}
