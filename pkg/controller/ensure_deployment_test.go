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
	"k8s.io/utils/ptr"

	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/queue/fake"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/config"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

func TestEnsureDeploymentHandler(t *testing.T) {
	now := metav1.Now()
	var nextKey handler.Key = "next"
	applyErr := fmt.Errorf("apply error")
	tests := []struct {
		name string

		migrationHash       string
		secretHash          string
		skipSecretHash      bool
		existingDeployments []*appsv1.Deployment
		pods                []*corev1.Pod
		currentStatus       *v1alpha1.SpiceDBCluster
		replicas            int32
		applyErr            error

		expectNext          handler.Key
		expectStatus        *v1alpha1.SpiceDBCluster
		expectRequeueErr    error
		expectRequeueAPIErr error
		expectRequeueAfter  bool
		expectApply         bool
		expectDelete        bool
		expectPatchStatus   bool
	}{
		{
			name:               "creates if no deployments",
			migrationHash:      "testtesttesttest",
			secretHash:         "secret",
			expectApply:        true,
			expectRequeueAfter: true,
		},
		{
			name:              "reports apply failure on status",
			migrationHash:     "testtesttesttest",
			secretHash:        "secret",
			applyErr:          applyErr,
			expectApply:       true,
			expectPatchStatus: true,
			expectStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
				Type:               v1alpha1.ConditionTypeRolloutError,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             v1alpha1.ConditionReasonApplyFailed,
				Message:            "Error applying deployment: apply error",
			}}}},
			expectRequeueAPIErr: applyErr,
		},
		{
			name: "clears apply failure once apply succeeds",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
				Type:               v1alpha1.ConditionTypeRolloutError,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             v1alpha1.ConditionReasonApplyFailed,
				Message:            "Error applying deployment: apply error",
			}}}},
			migrationHash:     "testtesttesttest",
			secretHash:        "secret",
			expectApply:       true,
			expectPatchStatus: true,
			expectStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{
				Conditions: []metav1.Condition{},
			}},
			expectRequeueAfter: true,
		},
		{
			// Regression test for https://github.com/authzed/spicedb-operator/issues/415
			// When all credential refs are configured with `skip: true`, ConfigChangedHandler
			// never sets CtxSecretHash because there is nothing to hash. The handler must
			// tolerate an unset/empty secret hash instead of panicking via MustValue on a
			// defaulting context key.
			name:               "creates if no deployments and secret hash is unset",
			migrationHash:      "testtesttesttest",
			skipSecretHash:     true,
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
				metadata.SpiceDBConfigKey: "e2dc14ae40f187f8",
			}}}},
			expectNext: nextKey,
		},
		{
			name:          "deletes extra deployments if a matching deployment exists",
			migrationHash: "testtesttesttest",
			secretHash:    "secret",
			existingDeployments: []*appsv1.Deployment{{}, {ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				metadata.SpiceDBConfigKey: "e2dc14ae40f187f8",
			}}}},
			expectDelete: true,
			expectNext:   nextKey,
		},
		{
			name:          "applies if secret changes",
			migrationHash: "testtesttesttest",
			secretHash:    "secret1",
			existingDeployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				metadata.SpiceDBConfigKey: "e2dc14ae40f187f8",
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
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{
				Conditions: []metav1.Condition{{
					Type:               v1alpha1.ConditionTypeRolling,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: now,
					Reason:             "WaitingForDeploymentAvailability",
					Message:            "Rolling deployment to latest version",
				}},
			}},
			existingDeployments: []*appsv1.Deployment{{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					metadata.SpiceDBConfigKey: "7ddaf0d5f794806f",
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
			expectStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{
				Conditions: []metav1.Condition{{
					Type:               v1alpha1.ConditionTypeRolling,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: now,
					Reason:             "WaitingForDeploymentAvailability",
					Message:            "Waiting for deployment to be available: 1/2 available, 1/2 ready, 1/2 updated, 0/0 generation.",
				}},
			}},
			expectRequeueAfter: true,
		},
		{
			name: "removes rollout status when deployment is available",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{
				Conditions: []metav1.Condition{{
					Type:               v1alpha1.ConditionTypeRolling,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: now,
					Reason:             "WaitingForDeploymentAvailability",
					Message:            "Waiting for deployment to be available: 1/2 available, 1/2 ready, 1/2 updated, 0/0 generation.",
				}},
			}},
			existingDeployments: []*appsv1.Deployment{{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					metadata.SpiceDBConfigKey: "7ddaf0d5f794806f",
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
			expectStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{
				Conditions: []metav1.Condition{},
			}},
		},
		{
			name: "reports error on status if pod has an error",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{
				Conditions: []metav1.Condition{{
					Type:               v1alpha1.ConditionTypeRolling,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: now,
					Reason:             "WaitingForDeploymentAvailability",
					Message:            "Rolling deployment to latest version",
				}},
			}},
			existingDeployments: []*appsv1.Deployment{{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					metadata.SpiceDBConfigKey: "7ddaf0d5f794806f",
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
			expectStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{
				Conditions: []metav1.Condition{{
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
				}},
			}},
			expectRequeueAfter: true,
		},
		{
			name: "updates error message if newer pod has a different message",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{
				Conditions: []metav1.Condition{{
					Type:               v1alpha1.ConditionTypeRolling,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: now,
					Reason:             "WaitingForDeploymentAvailability",
					Message:            "Rolling deployment to latest version",
				}},
			}},
			existingDeployments: []*appsv1.Deployment{{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					metadata.SpiceDBConfigKey: "7ddaf0d5f794806f",
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
			expectStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{
				Conditions: []metav1.Condition{{
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
				}},
			}},
			expectRequeueAfter: true,
		},
		{
			name: "removes error status if all pods are healthy",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{
				Conditions: []metav1.Condition{{
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
				}},
			}},
			existingDeployments: []*appsv1.Deployment{{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					metadata.SpiceDBConfigKey: "7ddaf0d5f794806f",
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
			expectStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{
				Conditions: []metav1.Condition{},
			}},
		},
		{
			name: "removes migrating failed status",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{
				Conditions: []metav1.Condition{
					v1alpha1.NewMigrationFailedCondition("postgres", "head", fmt.Errorf("err")),
				},
			}},
			existingDeployments: []*appsv1.Deployment{{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					metadata.SpiceDBConfigKey: "7ddaf0d5f794806f",
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
			expectStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{
				Conditions: []metav1.Condition{},
			}},
		},
		{
			// config.replicas is 2, but an autoscaler has scaled the Deployment
			// to 6 after spec.replicas was patched out of the operator's apply.
			// The rollout is judged against the live spec.replicas, so a fully
			// available Deployment at 6 clears the condition instead of waiting
			// forever for it to reach config.replicas.
			name: "removes rollout status when replicas are externally managed",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{
				Conditions: []metav1.Condition{{
					Type:               v1alpha1.ConditionTypeRolling,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: now,
					Reason:             "WaitingForDeploymentAvailability",
					Message:            "Waiting for deployment to be available: 6/2 available, 6/2 ready, 6/2 updated, 0/0 generation.",
				}},
			}},
			existingDeployments: []*appsv1.Deployment{{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					metadata.SpiceDBConfigKey: "7ddaf0d5f794806f",
				}},
				Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](6)},
				Status: appsv1.DeploymentStatus{
					Replicas:          6,
					UpdatedReplicas:   6,
					AvailableReplicas: 6,
					ReadyReplicas:     6,
				},
			}},
			replicas:          2,
			migrationHash:     "testtesttesttest",
			secretHash:        "secret",
			expectPatchStatus: true,
			expectNext:        nextKey,
			expectStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{
				Conditions: []metav1.Condition{},
			}},
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

			ctx := CtxConfig.WithValue(t.Context(), &config.Config{
				MigrationConfig: config.MigrationConfig{TargetSpiceDBImage: "test"},
				SpiceConfig:     config.SpiceConfig{Replicas: tt.replicas},
			})
			ctx = QueueOps.WithValue(ctx, ctrls)
			ctx = CtxCluster.WithValue(ctx, tt.currentStatus)
			ctx = CtxMigrationHash.WithValue(ctx, tt.migrationHash)
			if !tt.skipSecretHash {
				ctx = CtxSecretHash.WithValue(ctx, tt.secretHash)
			}
			ctx = CtxDeployments.WithValue(ctx, tt.existingDeployments)

			var called handler.Key
			h := &DeploymentHandler{
				applyDeployment: func(_ context.Context, _ *applyappsv1.DeploymentApplyConfiguration) (*appsv1.Deployment, error) {
					applyCalled = true
					return nil, tt.applyErr
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
			require.Equal(t, tt.expectApply, applyCalled, "apply's called status was different than expected")
			require.Equal(t, tt.expectDelete, deleteCalled)
			require.Equal(t, tt.expectPatchStatus, patchCalled)
			require.Equal(t, tt.expectNext, called)
			if tt.expectRequeueErr != nil {
				require.Equal(t, 1, ctrls.RequeueErrCallCount())
				require.Equal(t, tt.expectRequeueErr, ctrls.RequeueErrArgsForCall(0))
			}
			if tt.expectRequeueAPIErr != nil {
				require.Equal(t, 1, ctrls.RequeueAPIErrCallCount())
				require.Equal(t, tt.expectRequeueAPIErr, ctrls.RequeueAPIErrArgsForCall(0))
			}
			require.Equal(t, tt.expectRequeueAfter, ctrls.RequeueAfterCallCount() == 1)
		})
	}
}

func TestDesiredDeploymentReplicas(t *testing.T) {
	tests := []struct {
		name       string
		specValue  *int32
		configured int32
		expected   int32
	}{
		{name: "uses live spec.replicas when set", specValue: ptr.To[int32](6), configured: 2, expected: 6},
		{name: "falls back to configured when spec.replicas is unset", specValue: nil, configured: 2, expected: 2},
		{name: "uses live spec.replicas even when zero", specValue: ptr.To[int32](0), configured: 2, expected: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dep := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: tt.specValue}}
			require.Equal(t, tt.expected, desiredDeploymentReplicas(dep, tt.configured))
		})
	}
}

func TestDeploymentRolloutComplete(t *testing.T) {
	deployment := func(generation, observed int64, replicas, updated, available int32) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Generation: generation},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: observed,
				Replicas:           replicas,
				UpdatedReplicas:    updated,
				AvailableReplicas:  available,
			},
		}
	}
	tests := []struct {
		name     string
		dep      *appsv1.Deployment
		desired  int32
		expected bool
	}{
		{name: "complete when all replicas updated and available", dep: deployment(1, 1, 3, 3, 3), desired: 3, expected: true},
		{name: "incomplete while spec generation not yet observed", dep: deployment(2, 1, 3, 3, 3), desired: 3, expected: false},
		{name: "incomplete while replicas are still updating", dep: deployment(1, 1, 3, 2, 2), desired: 3, expected: false},
		{name: "incomplete while old replicas remain", dep: deployment(1, 1, 4, 3, 3), desired: 3, expected: false},
		{name: "incomplete while updated replicas are not yet available", dep: deployment(1, 1, 3, 3, 2), desired: 3, expected: false},
		{name: "complete against an externally scaled count above config", dep: deployment(1, 1, 6, 6, 6), desired: 6, expected: true},
		{name: "complete when scaled to zero", dep: deployment(1, 1, 0, 0, 0), desired: 0, expected: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, deploymentRolloutComplete(tt.dep, tt.desired))
		})
	}
}
