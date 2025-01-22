package controller

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/authzed/controller-idioms/typed"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

type keyRecordingQueue struct {
	workqueue.TypedRateLimitingInterface[any]
	Items chan any
}

func newKeyRecordingQueue(queue workqueue.TypedRateLimitingInterface[any]) *keyRecordingQueue {
	return &keyRecordingQueue{
		TypedRateLimitingInterface: queue,
		Items:                      make(chan any),
	}
}

func (q *keyRecordingQueue) Add(item any) {
	q.Items <- item
	q.TypedRateLimitingInterface.Add(item)
}

func (q *keyRecordingQueue) AddAfter(item any, d time.Duration) {
	q.Items <- item
	q.TypedRateLimitingInterface.AddAfter(item, d)
}

func (q *keyRecordingQueue) AddRateLimited(item any) {
	q.Items <- item
	q.TypedRateLimitingInterface.AddRateLimited(item)
}

func TestControllerNamespacing(t *testing.T) {
	tests := []struct {
		name              string
		watchedNamespaces []string
		createNamespaces  []string
		spiceDBClusters   map[string]string
		services          map[string]string
		expectedKeys      []string
	}{
		{
			name:              "default to all namespaces",
			watchedNamespaces: nil,
			createNamespaces:  []string{"test", "test2", "test3"},
			spiceDBClusters:   map[string]string{"test3": "test3"},
			services: map[string]string{
				"test":  "test",
				"test2": "test2",
			},
			expectedKeys: []string{
				"spicedbclusters.v1alpha1.authzed.com::test/test",
				"spicedbclusters.v1alpha1.authzed.com::test2/test2",
				"spicedbclusters.v1alpha1.authzed.com::test3/test3",
			},
		},
		{
			name:              "explicitly watch all namespaces",
			watchedNamespaces: []string{""},
			createNamespaces:  []string{"test", "test2", "test3"},
			spiceDBClusters:   map[string]string{"test3": "test3"},
			services: map[string]string{
				"test":  "test",
				"test2": "test2",
			},
			expectedKeys: []string{
				"spicedbclusters.v1alpha1.authzed.com::test/test",
				"spicedbclusters.v1alpha1.authzed.com::test2/test2",
				"spicedbclusters.v1alpha1.authzed.com::test3/test3",
			},
		},
		{
			name:              "watch one namespace (owned objects)",
			watchedNamespaces: []string{"test"},
			createNamespaces:  []string{"test", "test2", "test3"},
			spiceDBClusters:   map[string]string{"test3": "test3"},
			services: map[string]string{
				"test":  "test",
				"test2": "test2",
			},
			expectedKeys: []string{
				"spicedbclusters.v1alpha1.authzed.com::test/test",
			},
		},
		{
			name:              "watch one namespace (external objects)",
			watchedNamespaces: []string{"test2"},
			createNamespaces:  []string{"test", "test2", "test3"},
			spiceDBClusters:   map[string]string{"test3": "test3"},
			services: map[string]string{
				"test":  "test",
				"test2": "test2",
			},
			expectedKeys: []string{
				"spicedbclusters.v1alpha1.authzed.com::test2/test2",
			},
		},
		{
			name:              "watch multiple namespaces",
			watchedNamespaces: []string{"test2", "test3"},
			createNamespaces:  []string{"test", "test2", "test3"},
			spiceDBClusters:   map[string]string{"test3": "test3"},
			services: map[string]string{
				"test":  "test",
				"test2": "test2",
			},
			expectedKeys: []string{
				"spicedbclusters.v1alpha1.authzed.com::test2/test2",
				"spicedbclusters.v1alpha1.authzed.com::test3/test3",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			registry := typed.NewRegistry()
			broadcaster := record.NewBroadcaster()
			dclient := fake.NewSimpleDynamicClient(scheme.Scheme)
			kclient := kfake.NewSimpleClientset()
			c, err := NewController(ctx, registry, dclient, kclient, nil, "", broadcaster, tt.watchedNamespaces)
			require.NoError(t, err)
			queue := newKeyRecordingQueue(workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[any]()))
			c.Queue = queue
			go c.Start(ctx, 1)

			for _, ns := range tt.createNamespaces {
				ns, err := typed.ObjToUnstructuredObj(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
				require.NoError(t, err)
				_, err = dclient.Resource(corev1.SchemeGroupVersion.WithResource("namespaces")).Create(ctx, ns, metav1.CreateOptions{})
				require.NoError(t, err)
			}

			// test that non-owned objects (i.e. services) are watched in
			// the appropriate namespaces as well
			for ns, spicedb := range tt.services {
				service, err := typed.ObjToUnstructuredObj(&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test",
						Namespace: ns,
						Labels: map[string]string{
							metadata.OperatorManagedLabelKey: metadata.OperatorManagedLabelValue,
						},
						Annotations: map[string]string{
							metadata.OwnerAnnotationKeyPrefix + spicedb: "owned",
						},
					},
				})
				require.NoError(t, err)

				_, err = dclient.Resource(corev1.SchemeGroupVersion.WithResource("services")).Namespace(ns).Create(ctx, service, metav1.CreateOptions{})
				require.NoError(t, err)
			}

			for ns, spicedb := range tt.spiceDBClusters {
				service, err := typed.ObjToUnstructuredObj(&v1alpha1.SpiceDBCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name:      spicedb,
						Namespace: ns,
					},
				})
				require.NoError(t, err)

				_, err = dclient.Resource(v1alpha1ClusterGVR).Namespace(ns).Create(ctx, service, metav1.CreateOptions{})
				require.NoError(t, err)
			}

			gotKeys := make([]any, 0)
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case s := <-queue.Items:
						gotKeys = append(gotKeys, s)
						if len(gotKeys) == len(tt.expectedKeys) {
							return
						}
					case <-ctx.Done():
						return
					}
				}
			}()
			wg.Wait()
			require.ElementsMatch(t, tt.expectedKeys, gotKeys)
		})
	}
}
