package controller

import (
	"context"
	"fmt"
	"testing"

	"github.com/authzed/controller-idioms/pause"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/slices"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/queue/fake"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

func TestPauseHandler(t *testing.T) {
	var nextKey handler.Key = "next"
	tests := []struct {
		name string

		cluster    *v1alpha1.SpiceDBCluster
		patchError error

		expectNext        handler.Key
		expectEvents      []string
		expectPatchStatus bool
		expectConditions  []metav1.Condition
		expectRequeue     bool
		expectDone        bool
	}{
		{
			name: "pauses when label found",
			cluster: &v1alpha1.SpiceDBCluster{ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					metadata.PausedControllerSelectorKey: "",
				},
			}},
			expectPatchStatus: true,
			expectConditions:  []metav1.Condition{pause.NewPausedCondition(metadata.PausedControllerSelectorKey)},
			expectDone:        true,
		},
		{
			name: "requeues on pause patch error",
			cluster: &v1alpha1.SpiceDBCluster{ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					metadata.PausedControllerSelectorKey: "",
				},
			}},
			patchError:        fmt.Errorf("error patching"),
			expectPatchStatus: true,
			expectRequeue:     true,
		},
		{
			name: "no-op when label found and status is already paused",
			cluster: &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						metadata.PausedControllerSelectorKey: "",
					},
				},
				Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{pause.NewPausedCondition(metadata.PausedControllerSelectorKey)}},
			},
			expectDone: true,
		},
		{
			name: "removes condition when label is removed",
			cluster: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{
				pause.NewPausedCondition(metadata.PausedControllerSelectorKey),
			}}},
			expectPatchStatus: true,
			expectConditions:  []metav1.Condition{},
			expectNext:        nextKey,
		},
		{
			name: "requeues on unpause patch error",
			cluster: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{
				pause.NewPausedCondition(metadata.PausedControllerSelectorKey),
			}}},
			patchError:        fmt.Errorf("error patching"),
			expectPatchStatus: true,
			expectRequeue:     true,
		},
		{
			name: "unpauses a self-paused object",
			cluster: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{
				pause.NewSelfPausedCondition(metadata.PausedControllerSelectorKey),
			}}},
			expectPatchStatus: true,
			expectConditions:  []metav1.Condition{},
			expectNext:        nextKey,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrls := &fake.FakeInterface{}
			recorder := record.NewFakeRecorder(1)
			patchCalled := false

			ctx := context.Background()
			ctx = QueueOps.WithValue(ctx, ctrls)
			ctx = CtxClusterNN.WithValue(ctx, tt.cluster.NamespacedName())
			ctx = CtxClusterStatus.WithValue(ctx, tt.cluster)
			ctx = CtxCluster.WithValue(ctx, tt.cluster)
			var called handler.Key
			NewPauseHandler(func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error {
				patchCalled = true

				if tt.patchError != nil {
					return tt.patchError
				}

				require.Truef(t, slices.EqualFunc(tt.expectConditions, patch.Status.Conditions, func(a, b metav1.Condition) bool {
					return a.Type == b.Type &&
						a.Status == b.Status &&
						a.ObservedGeneration == b.ObservedGeneration &&
						a.Message == b.Message &&
						a.Reason == b.Reason
				}), "conditions not equal:\na: %#v\nb: %#v", tt.expectConditions, patch.Status.Conditions)

				return nil
			}, handler.ContextHandlerFunc(func(ctx context.Context) {
				called = nextKey
			})).Handle(ctx)

			require.Equal(t, tt.expectPatchStatus, patchCalled)
			require.Equal(t, tt.expectNext, called)
			require.Equal(t, tt.expectRequeue, ctrls.RequeueAPIErrCallCount() == 1)
			require.Equal(t, tt.expectDone, ctrls.DoneCallCount() == 1)
			ExpectEvents(t, recorder, tt.expectEvents)
		})
	}
}
