package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"

	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/queue/fake"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/config"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

func TestEnsureDeploymentHandler(t *testing.T) {
	now := metav1.Now()
	var nextKey handler.Key = "next"
	tests := []struct {
		name string

		migrationHash       string
		secretHash          string
		existingDeployments []*appsv1.Deployment
		pods                []*corev1.Pod
		currentStatus       *v1alpha1.SpiceDBCluster
		replicas            int32

		expectNext         handler.Key
		expectStatus       *v1alpha1.SpiceDBCluster
		expectRequeueErr   error
		expectRequeueAfter bool
		expectApply        bool
		expectDelete       bool
		expectPatchStatus  bool
	}{
		{
			name:               "creates if no deployments",
			migrationHash:      "testtesttesttest",
			secretHash:         "secret",
			expectApply:        true,
			expectRequeueAfter: true,
		},
		{
			name:                "creates if no matching deployment",
			migrationHash:       "testtesttesttest",
			secretHash:          "secret",
			existingDeployments: []*appsv1.Deployment{{}},
			expectApply:         true,
			expectRequeueAfter:  true,
		},
		{
			name:          "no-ops if one matching deployment",
			migrationHash: "testtesttesttest",
			secretHash:    "secret",
			existingDeployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				metadata.SpiceDBConfigKey: "n5ffh5b4h544h57bh649hbbh5fchb4q",
			}}}},
			expectNext: nextKey,
		},
		{
			name:          "deletes extra deployments if a matching deployment exists",
			migrationHash: "testtesttesttest",
			secretHash:    "secret",
			existingDeployments: []*appsv1.Deployment{{}, {ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				metadata.SpiceDBConfigKey: "n5ffh5b4h544h57bh649hbbh5fchb4q",
			}}}},
			expectDelete: true,
			expectNext:   nextKey,
		},
		{
			name:          "applies if secret changes",
			migrationHash: "testtesttesttest",
			secretHash:    "secret1",
			existingDeployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				metadata.SpiceDBConfigKey: "n5ffh5b4h544h57bh649hbbh5fchb4q",
			}}}},
			expectApply:        true,
			expectRequeueAfter: true,
		},
		{
			name: "removes migrating condition if present",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
				Type:   v1alpha1.ConditionTypeMigrating,
				Status: metav1.ConditionTrue,
			}}}},
			migrationHash:     "testtesttesttest",
			secretHash:        "secret",
			expectApply:       true,
			expectPatchStatus: true,
			expectStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
				Type:               v1alpha1.ConditionTypeRolling,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             "WaitingForDeploymentAvailability",
				Message:            "Rolling deployment to latest version",
			}}}},
			expectRequeueAfter: true,
		},
		{
			name: "waits if still rolling out",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
				Type:               v1alpha1.ConditionTypeRolling,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             "WaitingForDeploymentAvailability",
				Message:            "Rolling deployment to latest version",
			}}}},
			existingDeployments: []*appsv1.Deployment{{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					metadata.SpiceDBConfigKey: "n5fdh657h99h67dh57dh87h64dh5f8q",
				}},
				Status: appsv1.DeploymentStatus{
					Replicas:          2,
					UpdatedReplicas:   1,
					AvailableReplicas: 1,
					ReadyReplicas:     1,
				},
			}},
			replicas:          2,
			migrationHash:     "testtesttesttest",
			secretHash:        "secret",
			expectPatchStatus: true,
			expectStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
				Type:               v1alpha1.ConditionTypeRolling,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             "WaitingForDeploymentAvailability",
				Message:            "Waiting for deployment to be available: 1/2 available, 1/2 ready, 1/2 updated, 0/0 generation.",
			}}}},
			expectRequeueAfter: true,
		},
		{
			name: "removes rollout status when deployment is available",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
				Type:               v1alpha1.ConditionTypeRolling,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             "WaitingForDeploymentAvailability",
				Message:            "Waiting for deployment to be available: 1/2 available, 1/2 ready, 1/2 updated, 0/0 generation.",
			}}}},
			existingDeployments: []*appsv1.Deployment{{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					metadata.SpiceDBConfigKey: "n5fdh657h99h67dh57dh87h64dh5f8q",
				}},
				Status: appsv1.DeploymentStatus{
					Replicas:          2,
					UpdatedReplicas:   2,
					AvailableReplicas: 2,
					ReadyReplicas:     2,
				},
			}},
			replicas:          2,
			migrationHash:     "testtesttesttest",
			secretHash:        "secret",
			expectPatchStatus: true,
			expectNext:        nextKey,
			expectStatus:      &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{}}},
		},
		{
			name: "reports error on status if pod has an error",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
				Type:               v1alpha1.ConditionTypeRolling,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             "WaitingForDeploymentAvailability",
				Message:            "Rolling deployment to latest version",
			}}}},
			existingDeployments: []*appsv1.Deployment{{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					metadata.SpiceDBConfigKey: "n5fdh657h99h67dh57dh87h64dh5f8q",
				}},
				Status: appsv1.DeploymentStatus{
					Replicas:            2,
					UpdatedReplicas:     1,
					AvailableReplicas:   1,
					ReadyReplicas:       1,
					UnavailableReplicas: 1,
				},
			}},
			pods: []*corev1.Pod{{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				LastTerminationState: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						Message: "pod error",
					},
				},
			}}}}},
			replicas:          2,
			migrationHash:     "testtesttesttest",
			secretHash:        "secret",
			expectPatchStatus: true,
			expectStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
				Type:               v1alpha1.ConditionTypeRolling,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             "WaitingForDeploymentAvailability",
				Message:            "Rolling deployment to latest version",
			}, {
				Type:               v1alpha1.ConditionTypeRolloutError,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             "PodError",
				Message:            "pod error",
			}}}},
			expectRequeueAfter: true,
		},
		{
			name: "updates error message if newer pod has a different message",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
				Type:               v1alpha1.ConditionTypeRolling,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             "WaitingForDeploymentAvailability",
				Message:            "Rolling deployment to latest version",
			}}}},
			existingDeployments: []*appsv1.Deployment{{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					metadata.SpiceDBConfigKey: "n5fdh657h99h67dh57dh87h64dh5f8q",
				}},
				Status: appsv1.DeploymentStatus{
					Replicas:            2,
					UpdatedReplicas:     1,
					AvailableReplicas:   1,
					ReadyReplicas:       1,
					UnavailableReplicas: 1,
				},
			}},
			pods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(now.Add(1 * time.Hour))},
					Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
						LastTerminationState: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								Message: "pod error",
							},
						},
					}}},
				},
				{
					ObjectMeta: metav1.ObjectMeta{CreationTimestamp: now},
					Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
						LastTerminationState: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								Message: "new pod error",
							},
						},
					}}},
				},
			},
			replicas:          2,
			migrationHash:     "testtesttesttest",
			secretHash:        "secret",
			expectPatchStatus: true,
			expectStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
				Type:               v1alpha1.ConditionTypeRolling,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             "WaitingForDeploymentAvailability",
				Message:            "Rolling deployment to latest version",
			}, {
				Type:               v1alpha1.ConditionTypeRolloutError,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             "PodError",
				Message:            "new pod error",
			}}}},
			expectRequeueAfter: true,
		},
		{
			name: "removes error status if all pods are healthy",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
				Type:               v1alpha1.ConditionTypeRolling,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             "WaitingForDeploymentAvailability",
				Message:            "Rolling deployment to latest version",
			}, {
				Type:               v1alpha1.ConditionTypeRolloutError,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             "PodError",
				Message:            "pod error",
			}}}},
			existingDeployments: []*appsv1.Deployment{{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					metadata.SpiceDBConfigKey: "n5fdh657h99h67dh57dh87h64dh5f8q",
				}},
				Status: appsv1.DeploymentStatus{
					Replicas:          2,
					UpdatedReplicas:   2,
					AvailableReplicas: 2,
					ReadyReplicas:     2,
				},
			}},
			// still shows a last error, but the error isn't bubbled up
			// because ReadyReplicas matches the desired replicas
			pods: []*corev1.Pod{{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				LastTerminationState: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						Message: "pod error",
					},
				},
			}}}}},
			replicas:          2,
			migrationHash:     "testtesttesttest",
			secretHash:        "secret",
			expectPatchStatus: true,
			expectNext:        nextKey,
			expectStatus:      &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{}}},
		},
		{
			name: "removes migrating failed status",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{
				v1alpha1.NewMigrationFailedCondition("postgres", "head", fmt.Errorf("err")),
			}}},
			existingDeployments: []*appsv1.Deployment{{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					metadata.SpiceDBConfigKey: "n5fdh657h99h67dh57dh87h64dh5f8q",
				}},
				Status: appsv1.DeploymentStatus{
					Replicas:          2,
					UpdatedReplicas:   2,
					AvailableReplicas: 2,
					ReadyReplicas:     2,
				},
			}},
			replicas:          2,
			migrationHash:     "testtesttesttest",
			secretHash:        "secret",
			expectPatchStatus: true,
			expectNext:        nextKey,
			expectStatus:      &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrls := &fake.FakeInterface{}
			applyCalled := false
			deleteCalled := false
			patchCalled := false

			if tt.currentStatus == nil {
				tt.currentStatus = &v1alpha1.SpiceDBCluster{}
			}
			if tt.expectStatus == nil {
				tt.expectStatus = &v1alpha1.SpiceDBCluster{}
			}

			ctx := CtxConfig.WithValue(context.Background(), &config.Config{
				MigrationConfig: config.MigrationConfig{TargetSpiceDBImage: "test"},
				SpiceConfig:     config.SpiceConfig{Replicas: tt.replicas},
			})
			ctx = QueueOps.WithValue(ctx, ctrls)
			ctx = CtxCluster.WithValue(ctx, tt.currentStatus)
			ctx = CtxMigrationHash.WithValue(ctx, tt.migrationHash)
			ctx = CtxSecretHash.WithValue(ctx, tt.secretHash)
			ctx = CtxDeployments.WithValue(ctx, tt.existingDeployments)

			var called handler.Key
			h := &DeploymentHandler{
				applyDeployment: func(_ context.Context, _ *applyappsv1.DeploymentApplyConfiguration) (*appsv1.Deployment, error) {
					applyCalled = true
					return nil, nil
				},
				deleteDeployment: func(_ context.Context, _ types.NamespacedName) error {
					deleteCalled = true
					return nil
				},
				getDeploymentPods: func(_ context.Context) []*corev1.Pod {
					return tt.pods
				},
				patchStatus: func(_ context.Context, _ *v1alpha1.SpiceDBCluster) error {
					patchCalled = true
					return nil
				},
				next: handler.ContextHandlerFunc(func(_ context.Context) {
					called = nextKey
				}),
			}
			h.Handle(ctx)

			cluster := CtxCluster.MustValue(ctx)
			for i := range cluster.Status.Conditions {
				cluster.Status.Conditions[i].LastTransitionTime = now
			}
			require.Equal(t, tt.expectStatus, cluster)
			require.Equal(t, tt.expectApply, applyCalled)
			require.Equal(t, tt.expectDelete, deleteCalled)
			require.Equal(t, tt.expectPatchStatus, patchCalled)
			require.Equal(t, tt.expectNext, called)
			if tt.expectRequeueErr != nil {
				require.Equal(t, 1, ctrls.RequeueErrCallCount())
				require.Equal(t, tt.expectRequeueErr, ctrls.RequeueErrArgsForCall(0))
			}
			require.Equal(t, tt.expectRequeueAfter, ctrls.RequeueAfterCallCount() == 1)
		})
	}
}
