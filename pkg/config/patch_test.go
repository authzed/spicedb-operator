package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	openapitesting "k8s.io/kubectl/pkg/util/openapi/testing"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

func TestApplyPatches(t *testing.T) {
	runPatchTests(t, patchBasicTests)
	runPatchTests(t, patchFormatTests)
	runPatchTests(t, workloadIdentityPatchTests)
	runPatchTests(t, schedulerPatchTests)
	runPatchTests(t, fileMountTests)
}

type patchTestCase[K any] struct {
	name        string
	object      K
	out         K
	patches     []v1alpha1.Patch
	wantErr     error
	want        K
	wantPatched bool
	wantCount   int
}

func runPatchTests[K any](t *testing.T, cases []patchTestCase[K]) {
	// https://github.com/kubernetes/kubernetes/blob/v1.30.2/api/openapi-spec/swagger.json
	resources := openapitesting.NewFakeResources(filepath.Join("testdata", "swagger.1.30.2.json"))
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			count, patched, err := ApplyPatches(tt.object, tt.out, tt.patches, resources)
			if err != nil {
				fmt.Println(err.Error())
			}
			require.Equal(t, tt.wantErr, err)
			wantBytes, err := json.Marshal(tt.want)
			require.NoError(t, err)
			objectBytes, err := json.Marshal(tt.out)
			require.NoError(t, err)
			require.JSONEq(t, string(wantBytes), string(objectBytes))
			require.Equal(t, tt.wantPatched, patched)
			require.Equal(t, tt.wantCount, count)
		})
	}
}

var patchBasicTests = []patchTestCase[*applyappsv1.DeploymentApplyConfiguration]{
	{
		name:   "does nothing if kind doesn't match",
		object: applyappsv1.Deployment("test", "test"),
		out:    applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind: "ServiceAccount",
			Patch: json.RawMessage(`
metadata:
  labels:
    added: via-patch`),
		}},
		want:        applyappsv1.Deployment("test", "test"),
		wantPatched: false,
		wantCount:   0,
	},
	{
		name:   "does nothing if kind is omittied",
		object: applyappsv1.Deployment("test", "test"),
		out:    applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Patch: json.RawMessage(`
metadata:
  labels:
    added: via-patch`),
		}},
		want:        applyappsv1.Deployment("test", "test"),
		wantPatched: false,
		wantCount:   0,
	},
	{
		name:   "add two labels with separate patches",
		object: applyappsv1.Deployment("test", "test"),
		out:    applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{
			{
				Kind: "*",
				Patch: json.RawMessage(`
metadata:
  labels:
    added: via-patch`),
			},
			{
				Kind: "Deployment",
				Patch: json.RawMessage(`
metadata:
  labels:
    added2: via-patch2`),
			},
		},
		want: applyappsv1.Deployment("test", "test").
			WithLabels(map[string]string{"added": "via-patch", "added2": "via-patch2"}),
		wantPatched: true,
		wantCount:   2,
	},
	{
		name:   "later patches have higher precedence",
		object: applyappsv1.Deployment("test", "test"),
		out:    applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{
			{
				Kind: "*",
				Patch: json.RawMessage(`
metadata:
  labels:
    added: via-patch`),
			},
			{
				Kind: "Deployment",
				Patch: json.RawMessage(`
metadata:
  labels:
    added: via-patch2`),
			},
		},
		want: applyappsv1.Deployment("test", "test").
			WithLabels(map[string]string{"added": "via-patch2"}),
		wantPatched: true,
		wantCount:   2,
	},
}

var patchFormatTests = []patchTestCase[*applyappsv1.DeploymentApplyConfiguration]{
	{
		name:   "add labels to unpatchedDeployment (smp, yaml)",
		object: applyappsv1.Deployment("test", "test"),
		out:    applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind: "Deployment",
			Patch: json.RawMessage(`
metadata:
  labels:
    added: via-patch`),
		}},
		want: applyappsv1.Deployment("test", "test").
			WithLabels(map[string]string{"added": "via-patch"}),
		wantPatched: true,
		wantCount:   1,
	},
	{
		name:   "add labels to unpatchedDeployment (smp, json)",
		object: applyappsv1.Deployment("test", "test"),
		out:    applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind: "Deployment",
			Patch: json.RawMessage(`{
                  "metadata": {
                    "labels": {
                      "added": "via-patch"
                    }
                  }
                }`),
		}},
		want: applyappsv1.Deployment("test", "test").
			WithLabels(map[string]string{"added": "via-patch"}),
		wantPatched: true,
		wantCount:   1,
	},
	{
		name:   "add labels to unpatchedDeployment (JSON6902, yaml)",
		object: applyappsv1.Deployment("test", "test"),
		out:    applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind: "Deployment",
			Patch: json.RawMessage(`
  op: add
  path: /metadata/labels
  value: 
    added: via-patch`),
		}},
		want: applyappsv1.Deployment("test", "test").
			WithLabels(map[string]string{"added": "via-patch"}),
		wantPatched: true,
		wantCount:   1,
	},
	{
		name:   "add labels to unpatchedDeployment (JSON6902, json)",
		object: applyappsv1.Deployment("test", "test"),
		out:    applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind:  "Deployment",
			Patch: json.RawMessage(`{"op": "add", "path": "/metadata/labels", "value":{"added":"via-patch"}}`),
		}},
		want: applyappsv1.Deployment("test", "test").
			WithLabels(map[string]string{"added": "via-patch"}),
		wantPatched: true,
		wantCount:   1,
	},
	{
		name: "add labels to unpatchedDeployment without affecting existing labels (JSON6902, json)",
		object: applyappsv1.Deployment("test", "test").
			WithLabels(map[string]string{"initial": "label"}),
		out: applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind:  "Deployment",
			Patch: json.RawMessage(`{"op": "add", "path": "/metadata/labels/added", "value":"via-patch"}`),
		}},
		want: applyappsv1.Deployment("test", "test").WithLabels(
			map[string]string{
				"initial": "label",
				"added":   "via-patch",
			},
		),
		wantPatched: true,
		wantCount:   1,
	},
	{
		name: "add labels to unpatchedDeployment without affecting existing labels (smp, json)",
		object: applyappsv1.Deployment("test", "test").
			WithLabels(map[string]string{"initial": "label"}),
		out: applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind: "Deployment",
			Patch: json.RawMessage(`{
                  "metadata": {
                    "labels": {
                      "added": "via-patch"
                    }
                  }
                }`),
		}},
		want: applyappsv1.Deployment("test", "test").WithLabels(
			map[string]string{
				"initial": "label",
				"added":   "via-patch",
			},
		),
		wantPatched: true,
		wantCount:   1,
	},
	{
		name:   "add labels to unpatchedDeployment with a wildcard match (smp, yaml)",
		object: applyappsv1.Deployment("test", "test"),
		out:    applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind: "*",
			Patch: json.RawMessage(`
metadata:
  labels:
    added: via-patch`),
		}},
		want: applyappsv1.Deployment("test", "test").
			WithLabels(map[string]string{"added": "via-patch"}),
		wantPatched: true,
		wantCount:   1,
	},
}

var schedulerPatchTests = []patchTestCase[*applyappsv1.DeploymentApplyConfiguration]{
	{
		name:   "specify tolerations",
		object: baseDeployment(),
		out:    applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind: "Deployment",
			Patch: json.RawMessage(`
spec:
  template:
    spec:
      tolerations:
        - key: example-key
          operator: Exists
          effect: NoSchedule
`),
		}},
		want: applyappsv1.Deployment("test", "test").
			WithSpec(applyappsv1.DeploymentSpec().
				WithTemplate(applycorev1.PodTemplateSpec().
					WithSpec(applycorev1.PodSpec().WithContainers(
						applycorev1.Container().WithName("container"),
					).WithTolerations(applycorev1.Toleration().
						WithKey("example-key").
						WithOperator(corev1.TolerationOpExists).
						WithEffect(corev1.TaintEffectNoSchedule))),
				),
			),
		wantPatched: true,
		wantCount:   1,
	},
	{
		name:   "specify node selector",
		object: baseDeployment(),
		out:    applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind: "Deployment",
			Patch: json.RawMessage(`
spec:
  template:
    spec:
      nodeSelector:
        disktype: ssd
`),
		}},
		want: applyappsv1.Deployment("test", "test").
			WithSpec(applyappsv1.DeploymentSpec().
				WithTemplate(applycorev1.PodTemplateSpec().
					WithSpec(applycorev1.PodSpec().WithContainers(
						applycorev1.Container().WithName("container"),
					).WithNodeSelector(map[string]string{"disktype": "ssd"})),
				),
			),
		wantPatched: true,
		wantCount:   1,
	},
	{
		name:   "specify node name",
		object: baseDeployment(),
		out:    applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind: "Deployment",
			Patch: json.RawMessage(`
spec:
  template:
    spec:
      nodeName: kube-01
`),
		}},
		want: applyappsv1.Deployment("test", "test").
			WithSpec(applyappsv1.DeploymentSpec().
				WithTemplate(applycorev1.PodTemplateSpec().
					WithSpec(applycorev1.PodSpec().WithContainers(
						applycorev1.Container().WithName("container"),
					).WithNodeName("kube-01")),
				),
			),
		wantPatched: true,
		wantCount:   1,
	},
	{
		name:   "specify node affinity",
		object: baseDeployment(),
		out:    applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind: "Deployment",
			Patch: json.RawMessage(`
spec:
  template:
    spec:
      affinity:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
            - matchExpressions:
              - key: topology.kubernetes.io/zone
                operator: In
                values:
                - antarctica-east1
                - antarctica-west1
`),
		}},
		want: applyappsv1.Deployment("test", "test").
			WithSpec(applyappsv1.DeploymentSpec().
				WithTemplate(applycorev1.PodTemplateSpec().
					WithSpec(applycorev1.PodSpec().WithContainers(
						applycorev1.Container().WithName("container"),
					).WithAffinity(applycorev1.Affinity().WithNodeAffinity(
						applycorev1.NodeAffinity().WithRequiredDuringSchedulingIgnoredDuringExecution(
							applycorev1.NodeSelector().WithNodeSelectorTerms(
								applycorev1.NodeSelectorTerm().WithMatchExpressions(applycorev1.NodeSelectorRequirement().
									WithKey("topology.kubernetes.io/zone").
									WithOperator(corev1.NodeSelectorOpIn).
									WithValues("antarctica-east1", "antarctica-west1"))),
						)))),
				),
			),
		wantPatched: true,
		wantCount:   1,
	},
	{
		name:   "specify topology spread constraints",
		object: baseDeployment(),
		out:    applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind: "Deployment",
			Patch: json.RawMessage(`
spec:
  template:
    spec:
      topologySpreadConstraints:
      - maxSkew: 1
        topologyKey: kubernetes.io/hostname
        whenUnsatisfiable: DoNotSchedule
        matchLabelKeys:
        - app
        - pod-template-hash
`),
		}},
		want: applyappsv1.Deployment("test", "test").
			WithSpec(applyappsv1.DeploymentSpec().
				WithTemplate(applycorev1.PodTemplateSpec().
					WithSpec(applycorev1.PodSpec().WithContainers(
						applycorev1.Container().WithName("container"),
					).WithTopologySpreadConstraints(applycorev1.TopologySpreadConstraint().
						WithMaxSkew(1).
						WithTopologyKey("kubernetes.io/hostname").
						WithWhenUnsatisfiable(corev1.DoNotSchedule).
						WithMatchLabelKeys("app", "pod-template-hash"))),
				),
			),
		wantPatched: true,
		wantCount:   1,
	},
	{
		name:   "specify resource requests and limits",
		object: baseDeployment(),
		out:    applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind: "Deployment",
			Patch: json.RawMessage(`
spec:
  template:
    spec:
      containers:
      - name: container
        resources:
          requests:
            memory: "64Mi"
            cpu: "250m"
          limits:
            memory: "128Mi"
            cpu: "500m"
`),
		}},
		want: applyappsv1.Deployment("test", "test").
			WithSpec(applyappsv1.DeploymentSpec().
				WithTemplate(applycorev1.PodTemplateSpec().
					WithSpec(applycorev1.PodSpec().WithContainers(
						applycorev1.Container().WithName("container").
							WithResources(applycorev1.ResourceRequirements().
								WithRequests(corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("64Mi"),
									corev1.ResourceCPU:    resource.MustParse("250m"),
								}).
								WithLimits(corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("128Mi"),
									corev1.ResourceCPU:    resource.MustParse("500m"),
								})),
					)),
				),
			),
		wantPatched: true,
		wantCount:   1,
	},
	{
		name: "customize existing readiness probe",
		object: applyappsv1.Deployment("test", "test").
			WithSpec(applyappsv1.DeploymentSpec().
				WithTemplate(applycorev1.PodTemplateSpec().
					WithSpec(applycorev1.PodSpec().WithContainers(
						applycorev1.Container().
							WithName("container").
							WithReadinessProbe(applycorev1.Probe().
								WithGRPC(applycorev1.GRPCAction().
									WithPort(50051).
									WithService("spicedb")).
								WithFailureThreshold(2)),
					)),
				),
			),
		out: applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind: "Deployment",
			Patch: json.RawMessage(`
spec:
  template:
    spec:
      containers:
      - name: container
        readinessProbe:
          failureThreshold: 5
`),
		}},
		want: applyappsv1.Deployment("test", "test").
			WithSpec(applyappsv1.DeploymentSpec().
				WithTemplate(applycorev1.PodTemplateSpec().
					WithSpec(applycorev1.PodSpec().WithContainers(
						applycorev1.Container().
							WithName("container").
							WithReadinessProbe(applycorev1.Probe().
								WithGRPC(applycorev1.GRPCAction().
									WithPort(50051).
									WithService("spicedb")).
								WithFailureThreshold(5)),
					)),
				),
			),
		wantPatched: true,
		wantCount:   1,
	},
}

var fileMountTests = []patchTestCase[*applyappsv1.DeploymentApplyConfiguration]{
	{
		name:   "mount a file from a configmap",
		object: baseDeployment(),
		out:    applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind: "Deployment",
			Patch: json.RawMessage(`
spec:
  template:
    spec:
      volumes:
      - name:  config-volume
        configMap:
          name: special-config
      containers:
      - name: container
        volumeMounts:
        - name: config-volume
          mountPath: /etc/config
`),
		}},
		want: applyappsv1.Deployment("test", "test").
			WithSpec(applyappsv1.DeploymentSpec().
				WithTemplate(applycorev1.PodTemplateSpec().
					WithSpec(applycorev1.PodSpec().
						WithVolumes(applycorev1.Volume().
							WithName("config-volume").
							WithConfigMap(applycorev1.ConfigMapVolumeSource().
								WithName("special-config"))).
						WithContainers(applycorev1.Container().
							WithName("container").WithVolumeMounts(
							applycorev1.VolumeMount().
								WithName("config-volume").
								WithMountPath("/etc/config")),
						)),
				),
			),
		wantPatched: true,
		wantCount:   1,
	},
	{
		name: "mount a file from a secret, preserves preexisting volumes, json6902",
		object: applyappsv1.Deployment("test", "test").
			WithSpec(applyappsv1.DeploymentSpec().
				WithTemplate(applycorev1.PodTemplateSpec().
					WithSpec(applycorev1.PodSpec().
						WithVolumes(applycorev1.Volume().
							WithName("initial").
							WithConfigMap(applycorev1.ConfigMapVolumeSource().
								WithName("existing"))).
						WithContainers(applycorev1.Container().
							WithName("container").WithVolumeMounts(
							applycorev1.VolumeMount().
								WithName("initial").
								WithReadOnly(true).
								WithMountPath("/etc/config/existing")),
						)),
				),
			),
		out: applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind: "Deployment",
			Patch: json.RawMessage(`
			  {
				"op": "add",
				"path": "/spec/template/spec/containers/0/volumeMounts/-",
				"value": {
					"name": "config",
					"readOnly": true,
					"mountPath": "/etc/config/file"
				}
			  }`),
		}, {
			Kind: "Deployment",
			Patch: json.RawMessage(`
			  {
				"op": "add",
				"path": "/spec/template/spec/volumes/-",
				"value": {
					"name": "config",
					"secret": {
						"secretName": "secret"
					}
				}
			  }`),
		}},
		want: applyappsv1.Deployment("test", "test").
			WithSpec(applyappsv1.DeploymentSpec().
				WithTemplate(applycorev1.PodTemplateSpec().
					WithSpec(applycorev1.PodSpec().
						WithVolumes(
							applycorev1.Volume().
								WithName("initial").
								WithConfigMap(applycorev1.ConfigMapVolumeSource().
									WithName("existing")),
							applycorev1.Volume().
								WithName("config").
								WithSecret(applycorev1.SecretVolumeSource().
									WithSecretName("secret"))).
						WithContainers(applycorev1.Container().
							WithName("container").WithVolumeMounts(
							applycorev1.VolumeMount().
								WithName("initial").
								WithReadOnly(true).
								WithMountPath("/etc/config/existing"),
							applycorev1.VolumeMount().
								WithName("config").
								WithReadOnly(true).
								WithMountPath("/etc/config/file")),
						)),
				),
			),
		wantPatched: true,
		wantCount:   2,
	},
	{
		name: "mount a file from a secret, preserves preexisting volumes, SMP",
		object: applyappsv1.Deployment("test", "test").
			WithSpec(applyappsv1.DeploymentSpec().
				WithTemplate(applycorev1.PodTemplateSpec().
					WithSpec(applycorev1.PodSpec().
						WithVolumes(applycorev1.Volume().
							WithName("initial").
							WithConfigMap(applycorev1.ConfigMapVolumeSource().
								WithName("existing"))).
						WithContainers(applycorev1.Container().
							WithName("container").WithVolumeMounts(
							applycorev1.VolumeMount().
								WithName("initial").
								WithReadOnly(true).
								WithMountPath("/etc/config/existing")),
						)),
				),
			),
		out: applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind: "Deployment",
			Patch: json.RawMessage(`
spec:
  template:
    spec:
      volumes:
      - name:  config-volume
        configMap:
          name: special-config
      containers:
      - name: container
        volumeMounts:
        - name: config-volume
          mountPath: /etc/config
`),
		}},
		want: applyappsv1.Deployment("test", "test").
			WithSpec(applyappsv1.DeploymentSpec().
				WithTemplate(applycorev1.PodTemplateSpec().
					WithSpec(applycorev1.PodSpec().
						WithVolumes(
							applycorev1.Volume().
								WithName("config-volume").
								WithConfigMap(applycorev1.ConfigMapVolumeSource().
									WithName("special-config")),
							applycorev1.Volume().
								WithName("initial").
								WithConfigMap(applycorev1.ConfigMapVolumeSource().
									WithName("existing")),
						).WithContainers(applycorev1.Container().
						WithName("container").WithVolumeMounts(
						applycorev1.VolumeMount().
							WithName("config-volume").
							WithMountPath("/etc/config"),
						applycorev1.VolumeMount().
							WithName("initial").
							WithReadOnly(true).
							WithMountPath("/etc/config/existing"),
					))),
				),
			),
		wantPatched: true,
		wantCount:   1,
	},
	{
		name:   "mount secret from aws with csi secret driver",
		object: baseDeployment(),
		out:    applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind: "Deployment",
			Patch: json.RawMessage(`
spec:
  template:
    spec:
      volumes:
      - name: secrets-store-inline
        csi:
          driver: secrets-store.csi.k8s.io
          readOnly: true
          volumeAttributes:
            secretProviderClass: "spicedb-aws-secrets"
      containers:
      - name: container
        volumeMounts:
        - name: secrets-store-inline
          mountPath: "/mnt/secrets-store"
          readOnly: true
`),
		}},
		want: applyappsv1.Deployment("test", "test").
			WithSpec(applyappsv1.DeploymentSpec().
				WithTemplate(applycorev1.PodTemplateSpec().
					WithSpec(applycorev1.PodSpec().
						WithVolumes(applycorev1.Volume().
							WithName("secrets-store-inline").
							WithCSI(applycorev1.CSIVolumeSource().
								WithDriver("secrets-store.csi.k8s.io").
								WithReadOnly(true).
								WithVolumeAttributes(map[string]string{"secretProviderClass": "spicedb-aws-secrets"}))).
						WithContainers(applycorev1.Container().
							WithName("container").WithVolumeMounts(
							applycorev1.VolumeMount().
								WithName("secrets-store-inline").
								WithMountPath("/mnt/secrets-store").
								WithReadOnly(true)),
						)),
				),
			),
		wantPatched: true,
		wantCount:   1,
	},
	{
		name: "mount multiple volumes in multiple patches with multiple existing volumes, SMP",
		object: applyappsv1.Deployment("test", "test").
			WithSpec(applyappsv1.DeploymentSpec().
				WithTemplate(applycorev1.PodTemplateSpec().
					WithSpec(applycorev1.PodSpec().
						WithVolumes(
							applycorev1.Volume().WithName("initial").
								WithConfigMap(applycorev1.ConfigMapVolumeSource().
									WithName("existing")),
							applycorev1.Volume().WithName("labels").
								WithDownwardAPI(applycorev1.DownwardAPIVolumeSource().WithItems(
									applycorev1.DownwardAPIVolumeFile().
										WithPath("labels").
										WithFieldRef(applycorev1.ObjectFieldSelector().
											WithFieldPath("metadata.labels"),
										),
								)),
						).
						WithContainers(applycorev1.Container().
							WithName("container").WithVolumeMounts(
							applycorev1.VolumeMount().
								WithName("initial").
								WithReadOnly(true).
								WithMountPath("/etc/config/existing"),
							applycorev1.VolumeMount().
								WithName("labels").
								WithMountPath("/etc/podlabels"),
						),
						)),
				),
			),
		out: applyappsv1.Deployment("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind: "Deployment",
			Patch: json.RawMessage(`
spec:
  template:
    spec:
      volumes:
      - name:  config-volume
        configMap:
          name: special-config
      containers:
      - name: container
        volumeMounts:
        - name: config-volume
          mountPath: /etc/config
`),
		}, {
			Kind: "Deployment",
			Patch: json.RawMessage(`
spec:
  template:
    spec:
      volumes:
      - name: secrets-store-inline
        csi:
          driver: secrets-store.csi.k8s.io
          readOnly: true
          volumeAttributes:
            secretProviderClass: "spicedb-aws-secrets"
      containers:
      - name: container
        volumeMounts:
        - name: secrets-store-inline
          mountPath: "/mnt/secrets-store"
          readOnly: true
`),
		}},
		want: applyappsv1.Deployment("test", "test").
			WithSpec(applyappsv1.DeploymentSpec().
				WithTemplate(applycorev1.PodTemplateSpec().
					WithSpec(applycorev1.PodSpec().
						WithVolumes(
							applycorev1.Volume().
								WithName("secrets-store-inline").
								WithCSI(applycorev1.CSIVolumeSource().
									WithDriver("secrets-store.csi.k8s.io").
									WithReadOnly(true).
									WithVolumeAttributes(map[string]string{"secretProviderClass": "spicedb-aws-secrets"})),
							applycorev1.Volume().
								WithName("config-volume").
								WithConfigMap(applycorev1.ConfigMapVolumeSource().
									WithName("special-config")),
							applycorev1.Volume().
								WithName("initial").
								WithConfigMap(applycorev1.ConfigMapVolumeSource().
									WithName("existing")),
							applycorev1.Volume().WithName("labels").
								WithDownwardAPI(applycorev1.DownwardAPIVolumeSource().WithItems(
									applycorev1.DownwardAPIVolumeFile().
										WithPath("labels").
										WithFieldRef(applycorev1.ObjectFieldSelector().
											WithFieldPath("metadata.labels"),
										),
								)),
						).WithContainers(applycorev1.Container().
						WithName("container").WithVolumeMounts(
						applycorev1.VolumeMount().
							WithName("secrets-store-inline").
							WithMountPath("/mnt/secrets-store").
							WithReadOnly(true),
						applycorev1.VolumeMount().
							WithName("config-volume").
							WithMountPath("/etc/config"),
						applycorev1.VolumeMount().
							WithName("initial").
							WithReadOnly(true).
							WithMountPath("/etc/config/existing"),
						applycorev1.VolumeMount().
							WithName("labels").
							WithMountPath("/etc/podlabels"),
					))),
				),
			),
		wantPatched: true,
		wantCount:   2,
	},
}

var workloadIdentityPatchTests = []patchTestCase[*applycorev1.ServiceAccountApplyConfiguration]{
	{
		name:   "add annotations to a serviceaccount",
		object: applycorev1.ServiceAccount("test", "test"),
		out:    applycorev1.ServiceAccount("test", "test"),
		patches: []v1alpha1.Patch{{
			Kind: "ServiceAccount",
			Patch: json.RawMessage(`
metadata:
  annotations:
    added: via-patch`),
		}},
		want:        applycorev1.ServiceAccount("test", "test").WithAnnotations(map[string]string{"added": "via-patch"}),
		wantPatched: true,
		wantCount:   1,
	},
}

func baseDeployment() *applyappsv1.DeploymentApplyConfiguration {
	return applyappsv1.Deployment("test", "test").
		WithSpec(applyappsv1.DeploymentSpec().
			WithTemplate(applycorev1.PodTemplateSpec().
				WithSpec(applycorev1.PodSpec().WithContainers(
					applycorev1.Container().WithName("container"),
				)),
			),
		)
}
