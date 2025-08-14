package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/authzed/controller-idioms/hash"
	jsonpatch "github.com/evanphx/json-patch"
	"github.com/fatih/camelcase"
	"github.com/gosimple/slug"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	applybatchv1 "k8s.io/client-go/applyconfigurations/batch/v1"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	applymetav1 "k8s.io/client-go/applyconfigurations/meta/v1"
	applypolicyv1 "k8s.io/client-go/applyconfigurations/policy/v1"
	applyrbacv1 "k8s.io/client-go/applyconfigurations/rbac/v1"
	"k8s.io/kubectl/pkg/util/openapi"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

const (
	Head = "head"

	dbTLSVolume        = "db-tls"
	spannerVolume      = "spanner"
	tlsVolume          = "tls"
	dispatchTLSVolume  = "dispatch-tls"
	telemetryTLSVolume = "telemetry-tls"
	labelsVolume       = "podlabels"
	annotationsVolume  = "podannotations"
	podNameVolume      = "podname"

	DefaultTLSKeyFile = "/tls/tls.key"
	DefaultTLSCrtFile = "/tls/tls.crt"

	// nolint:gosec // Creds in the naming causes a false positive here.
	spannerCredsPath     = "/spanner-credentials"
	spannerCredsFileName = "credentials.json"

	ContainerNameSpiceDB = "spicedb"
)

type key[V comparable] struct {
	key          string
	defaultValue V
}

var (
	imageKey                          = newStringKey("image")
	projectLabels                     = newBoolOrStringKey("projectLabels", true)
	projectAnnotations                = newBoolOrStringKey("projectAnnotations", true)
	tlsSecretNameKey                  = newStringKey("tlsSecretName")
	dispatchCAKey                     = newStringKey("dispatchUpstreamCASecretName")
	dispatchCAFilePathKey             = newKey("dispatchUpstreamCAFilePath", "tls.crt")
	dispatchEnabledKey                = newBoolOrStringKey("dispatchEnabled", true)
	telemetryCAKey                    = newStringKey("telemetryCASecretName")
	envPrefixKey                      = newKey("envPrefix", "SPICEDB")
	spiceDBCmdKey                     = newKey("cmd", "spicedb")
	skipMigrationsKey                 = newBoolOrStringKey("skipMigrations", false)
	targetMigrationKey                = newStringKey("targetMigration")
	targetPhase                       = newStringKey("datastoreMigrationPhase")
	logLevelKey                       = newKey("logLevel", "info")
	migrationLogLevelKey              = newKey("migrationLogLevel", "debug")
	spannerCredentialsKey             = newStringKey("spannerCredentials")
	datastoreTLSSecretKey             = newStringKey("datastoreTLSSecretName")
	datastoreEngineKey                = newStringKey("datastoreEngine")
	replicasKey                       = newIntOrStringKey[int32]("replicas", 2)
	replicasKeyForMemory              = newIntOrStringKey[int32]("replicas", 1)
	extraPodLabelsKey                 = metadataSetKey("extraPodLabels")
	extraPodAnnotationsKey            = metadataSetKey("extraPodAnnotations")
	extraServiceAccountAnnotationsKey = metadataSetKey("extraServiceAccountAnnotations")
	serviceAccountNameKey             = newStringKey("serviceAccountName")
	grpcTLSKeyPathKey                 = newKey("grpcTLSKeyPath", DefaultTLSKeyFile)
	grpcTLSCertPathKey                = newKey("grpcTLSCertPath", DefaultTLSCrtFile)
	dispatchClusterTLSKeyPathKey      = newKey("dispatchClusterTLSKeyPath", DefaultTLSKeyFile)
	dispatchClusterTLSCertPathKey     = newKey("dispatchClusterTLSCertPath", DefaultTLSCrtFile)
	httpTLSKeyPathKey                 = newKey("httpTLSKeyPath", DefaultTLSKeyFile)
	httpTLSCertPathKey                = newKey("httpTLSCertPath", DefaultTLSCrtFile)
	dashboardTLSKeyPathKey            = newKey("dashboardTLSKeyPath", DefaultTLSKeyFile)
	dashboardTLSCertPathKey           = newKey("dashboardTLSCertPath", DefaultTLSCrtFile)
)

// Warning is an issue with configuration that we will report as undesirable
// but which don't prevent the cluster from starting (i.e. no TLS config)
type Warning error

// RawConfig has not been processed/validated yet
type RawConfig map[string]any

func (r RawConfig) Pop(key string) string {
	v, ok := r[key]
	if !ok {
		return ""
	}
	delete(r, key)

	// Handle string values
	if vs, ok := v.(string); ok {
		return vs
	}

	// Handle boolean values by converting to string
	if vb, ok := v.(bool); ok {
		return strconv.FormatBool(vb)
	}

	// Handle other types by converting to string
	return fmt.Sprintf("%v", v)
}

// Config holds all values required to create and manage a cluster.
// Note: The config object holds values from referenced secrets for
// hashing purposes; these should not be used directly (instead the secret
// should be mounted)
type Config struct {
	MigrationConfig
	SpiceConfig
	Patches   []v1alpha1.Patch
	Resources openapi.Resources
}

// MigrationConfig stores data that is relevant for running migrations
// or deciding if migrations need to be run
type MigrationConfig struct {
	TargetMigration        string
	TargetPhase            string
	MigrationLogLevel      string
	DatastoreEngine        string
	DatastoreURI           string
	SpannerCredsSecretRef  string
	TargetSpiceDBImage     string
	EnvPrefix              string
	SpiceDBCmd             string
	DatastoreTLSSecretName string
	SpiceDBVersion         *v1alpha1.SpiceDBVersion
}

// SpiceConfig contains config relevant to running spicedb or determining
// if spicedb needs to be updated
type SpiceConfig struct {
	LogLevel                       string
	SkipMigrations                 bool
	Name                           string
	Namespace                      string
	UID                            string
	Replicas                       int32
	PresharedKey                   string
	EnvPrefix                      string
	SpiceDBCmd                     string
	TLSSecretName                  string
	DispatchEnabled                bool
	DispatchUpstreamCASecretName   string
	DispatchUpstreamCASecretPath   string
	TelemetryTLSCASecretName       string
	SecretName                     string
	ExtraPodLabels                 map[string]string
	ExtraPodAnnotations            map[string]string
	ExtraServiceAccountAnnotations map[string]string
	ServiceAccountName             string
	ProjectLabels                  bool
	ProjectAnnotations             bool
	Passthrough                    map[string]string
}

// NewConfig checks that the values in the config + the secret are sane
func NewConfig(cluster *v1alpha1.SpiceDBCluster, globalConfig *OperatorConfig, secret *corev1.Secret, resources openapi.Resources) (*Config, Warning, error) {
	if cluster.Spec.Config == nil {
		return nil, nil, fmt.Errorf("couldn't parse empty config")
	}

	config := RawConfig(make(map[string]any))
	if err := json.Unmarshal(cluster.Spec.Config, &config); err != nil {
		return nil, nil, fmt.Errorf("couldn't parse config: %w", err)
	}

	passthroughConfig := make(map[string]string)
	errs := make([]error, 0)
	warnings := make([]error, 0)

	spiceConfig := SpiceConfig{
		Name:                         cluster.Name,
		Namespace:                    cluster.Namespace,
		UID:                          string(cluster.UID),
		TLSSecretName:                tlsSecretNameKey.pop(config),
		ServiceAccountName:           serviceAccountNameKey.pop(config),
		DispatchUpstreamCASecretName: dispatchCAKey.pop(config),
		DispatchUpstreamCASecretPath: dispatchCAFilePathKey.pop(config),
		TelemetryTLSCASecretName:     telemetryCAKey.pop(config),
		EnvPrefix:                    envPrefixKey.pop(config),
		SpiceDBCmd:                   spiceDBCmdKey.pop(config),
		LogLevel:                     logLevelKey.pop(config),
	}
	migrationConfig := MigrationConfig{
		MigrationLogLevel:      migrationLogLevelKey.pop(config),
		SpannerCredsSecretRef:  spannerCredentialsKey.pop(config),
		EnvPrefix:              spiceConfig.EnvPrefix,
		SpiceDBCmd:             spiceConfig.SpiceDBCmd,
		DatastoreTLSSecretName: datastoreTLSSecretKey.pop(config),
		TargetMigration:        targetMigrationKey.pop(config),
		TargetPhase:            targetPhase.pop(config),
	}

	datastoreEngine := datastoreEngineKey.pop(config)
	if len(datastoreEngine) == 0 {
		errs = append(errs, fmt.Errorf("datastoreEngine is a required field"))
	}

	var err error
	spiceConfig.ProjectLabels, err = projectLabels.pop(config)
	if err != nil {
		warnings = append(warnings, fmt.Errorf("defaulting to false: %w", err))
	}
	spiceConfig.ProjectAnnotations, err = projectAnnotations.pop(config)
	if err != nil {
		warnings = append(warnings, fmt.Errorf("defaulting to false: %w", err))
	}

	// if there's a required edge from the current image, that edge is taken
	// unless the current config is equal to the input.
	image := imageKey.pop(config)

	baseImage, targetSpiceDBVersion, state, err := globalConfig.ComputeTarget(globalConfig.ImageName, cluster.Spec.BaseImage, image, cluster.Spec.Version, cluster.Spec.Channel, datastoreEngine, cluster.Status.CurrentVersion, cluster.RolloutInProgress())
	if err != nil {
		errs = append(errs, err)
	}

	migrationConfig.SpiceDBVersion = targetSpiceDBVersion
	migrationConfig.TargetPhase = state.Phase
	migrationConfig.TargetMigration = state.Migration
	if len(migrationConfig.TargetMigration) == 0 {
		migrationConfig.TargetMigration = Head
	}

	if len(spiceConfig.ServiceAccountName) == 0 {
		spiceConfig.ServiceAccountName = cluster.Name
	}

	switch {
	case len(state.Digest) > 0:
		migrationConfig.TargetSpiceDBImage = baseImage + "@" + state.Digest
	case len(state.Tag) > 0:
		migrationConfig.TargetSpiceDBImage = baseImage + ":" + state.Tag
	default:
		errs = append(errs, fmt.Errorf("no update found in channel"))
	}

	spiceConfig.DispatchEnabled, err = dispatchEnabledKey.pop(config)
	if err != nil {
		errs = append(errs, err)
	}

	// can't run dispatch with memory datastore
	if datastoreEngine == "memory" {
		spiceConfig.DispatchEnabled = false
	}

	migrationConfig.DatastoreEngine = datastoreEngine
	passthroughConfig["datastoreEngine"] = datastoreEngine
	passthroughConfig["dispatchClusterEnabled"] = strconv.FormatBool(spiceConfig.DispatchEnabled)

	if secret == nil {
		errs = append(errs, fmt.Errorf("secret must be provided"))
	}

	var datastoreURI, psk []byte
	if secret != nil {
		spiceConfig.SecretName = secret.GetName()

		var ok bool
		datastoreURI, ok = secret.Data["datastore_uri"]
		if !ok && datastoreEngine != "memory" {
			errs = append(errs, fmt.Errorf("secret must contain a datastore_uri field"))
		}
		migrationConfig.DatastoreURI = string(datastoreURI)
		psk, ok = secret.Data["preshared_key"]
		if !ok {
			errs = append(errs, fmt.Errorf("secret must contain a preshared_key field"))
		}
		spiceConfig.PresharedKey = string(psk)
	}

	if len(migrationConfig.SpannerCredsSecretRef) > 0 {
		passthroughConfig["datastoreSpannerCredentials"] = filepath.Join(spannerCredsPath, spannerCredsFileName)
	}

	selectedReplicaKey := replicasKey
	if datastoreEngine == "memory" {
		selectedReplicaKey = replicasKeyForMemory
	}

	replicas, err := selectedReplicaKey.pop(config)
	if err != nil {
		errs = append(errs, fmt.Errorf("invalid value for replicas %q: %w", replicas, err))
	}

	spiceConfig.Replicas = replicas
	if replicas > 1 && datastoreEngine == "memory" {
		errs = append(errs, fmt.Errorf("cannot set replicas > 1 for memory engine"))
	}
	spiceConfig.SkipMigrations, err = skipMigrationsKey.pop(config)
	if err != nil {
		errs = append(errs, err)
	}

	var labelWarnings []error
	spiceConfig.ExtraPodLabels, labelWarnings, err = extraPodLabelsKey.pop(config, "pod", "label")
	if err != nil {
		errs = append(errs, err)
	}

	if len(labelWarnings) > 0 {
		warnings = append(warnings, labelWarnings...)
	}

	var annotationWarnings []error
	spiceConfig.ExtraPodAnnotations, annotationWarnings, err = extraPodAnnotationsKey.pop(config, "pod", "annotation")
	if err != nil {
		errs = append(errs, err)
	}
	if len(annotationWarnings) > 0 {
		warnings = append(warnings, annotationWarnings...)
	}

	var saAnnotationWarnings []error
	spiceConfig.ExtraServiceAccountAnnotations, saAnnotationWarnings, err = extraServiceAccountAnnotationsKey.pop(config, "service account", "annotation")
	if err != nil {
		errs = append(errs, err)
	}
	if len(saAnnotationWarnings) > 0 {
		warnings = append(warnings, saAnnotationWarnings...)
	}

	// generate secret refs for tls if specified
	if len(spiceConfig.TLSSecretName) > 0 {
		passthroughKeys := []*key[string]{
			grpcTLSKeyPathKey,
			grpcTLSCertPathKey,
			dispatchClusterTLSKeyPathKey,
			dispatchClusterTLSCertPathKey,
			httpTLSKeyPathKey,
			httpTLSCertPathKey,
			dashboardTLSKeyPathKey,
			dashboardTLSCertPathKey,
		}
		for _, k := range passthroughKeys {
			passthroughConfig[k.key] = k.pop(config)
		}
	} else {
		warnings = append(warnings, fmt.Errorf("no TLS configured, consider setting %q", "tlsSecretName"))
	}

	if len(spiceConfig.DispatchUpstreamCASecretName) > 0 && spiceConfig.DispatchEnabled {
		passthroughConfig["dispatchUpstreamCAPath"] = "/dispatch-tls/" + spiceConfig.DispatchUpstreamCASecretPath
	}

	if len(spiceConfig.TelemetryTLSCASecretName) > 0 {
		passthroughConfig["telemetryCAOverridePath"] = "/telemetry-tls/tls.crt"
	}

	// set targetMigrationPhase if needed
	if len(migrationConfig.TargetPhase) > 0 {
		passthroughConfig["datastoreMigrationPhase"] = migrationConfig.TargetPhase
	}

	// the rest of the config is passed through to spicedb as strings
	for k := range config {
		passthroughConfig[k] = config.Pop(k)
	}

	stripValues := []string{
		"datastoreConnUri",
		"grpcPresharedKey",
		"presharedKey",
		"preshared_key",
		"datastore_uri",
	}
	// strip sensitive values from passthrough config (if they have been
	// inadvertently set by a user)
	for k := range passthroughConfig {
		for _, s := range stripValues {
			if strings.EqualFold(k, s) {
				delete(passthroughConfig, k)
			}
		}
	}

	// set the termination log path to the kube default
	if len(passthroughConfig["terminationLogPath"]) == 0 {
		passthroughConfig["terminationLogPath"] = "/dev/termination-log"
	}

	spiceConfig.Passthrough = passthroughConfig

	out := &Config{
		MigrationConfig: migrationConfig,
		SpiceConfig:     spiceConfig,
		Resources:       resources,
	}
	out.Patches = fixDeploymentPatches(out.Name, cluster.Spec.Patches)

	// Validate that patches apply cleanly ahead of time
	totalAppliedPatches := 0
	for _, obj := range []any{
		out.unpatchedServiceAccount(),
		out.unpatchedRole(),
		out.unpatchedRoleBinding(),
		out.unpatchedService(),
		out.unpatchedMigrationJob(hash.Object("")),
		out.unpatchedDeployment(hash.Object(""), hash.Object("")),
	} {
		applied, diff, err := ApplyPatches(obj, obj, out.Patches, resources)
		if err != nil {
			errs = append(errs, err)
		}

		if applied > 0 && !diff {
			warnings = append(warnings, fmt.Errorf("patches applied to object, but there were no changes to the object"))
		}

		totalAppliedPatches += applied
	}

	if totalAppliedPatches < len(out.Patches) {
		warnings = append(warnings, fmt.Errorf("only %d/%d patches applied successfully", totalAppliedPatches, len(out.Patches)))
	}

	warning := Warning(errors.NewAggregate(warnings))
	if len(errs) > 0 {
		return nil, warning, errors.NewAggregate(errs)
	}

	return out, warning, nil
}

// toEnvVarApplyConfiguration returns a set of env variables to apply to a
// spicedb container
func (c *Config) toEnvVarApplyConfiguration() []*applycorev1.EnvVarApplyConfiguration {
	// Set non-passthrough config that is either generated directly by the
	// controller (dispatch address), has some direct effect on the cluster
	// (tls), or lives in an external secret (preshared key).
	envVars := []*applycorev1.EnvVarApplyConfiguration{
		applycorev1.EnvVar().WithName(c.SpiceConfig.EnvPrefix + "_POD_NAME").WithValueFrom(
			applycorev1.EnvVarSource().WithFieldRef(applycorev1.ObjectFieldSelector().WithFieldPath("metadata.name"))),
		applycorev1.EnvVar().WithName(c.SpiceConfig.EnvPrefix + "_LOG_LEVEL").WithValue(c.LogLevel),
		applycorev1.EnvVar().WithName(c.SpiceConfig.EnvPrefix + "_GRPC_PRESHARED_KEY").
			WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(
				applycorev1.SecretKeySelector().WithName(c.SecretName).WithKey("preshared_key"))),
	}
	if c.DatastoreEngine != "memory" {
		envVars = append(envVars,
			applycorev1.EnvVar().WithName(c.SpiceConfig.EnvPrefix+"_DATASTORE_CONN_URI").WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(
				applycorev1.SecretKeySelector().WithName(c.SecretName).WithKey("datastore_uri"))))
	}
	if c.DispatchEnabled {
		envVars = append(envVars,
			applycorev1.EnvVar().WithName(c.SpiceConfig.EnvPrefix+"_DISPATCH_UPSTREAM_ADDR").
				WithValue(fmt.Sprintf("kubernetes:///%s.%s:dispatch", c.Name, c.Namespace)))
	}

	// Passthrough config is user-provided and only affects spicedb runtime.
	keys := make([]string, 0, len(c.Passthrough))
	for k := range c.Passthrough {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		envVars = append(envVars, applycorev1.EnvVar().
			WithName(toEnvVarName(c.SpiceConfig.EnvPrefix, k)).WithValue(c.Passthrough[k]))
	}

	return envVars
}

func (c *Config) ownerRef() *applymetav1.OwnerReferenceApplyConfiguration {
	return applymetav1.OwnerReference().
		WithName(c.Name).
		WithKind(v1alpha1.SpiceDBClusterKind).
		WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
		WithUID(types.UID(c.UID))
}

func (c *Config) unpatchedServiceAccount() *applycorev1.ServiceAccountApplyConfiguration {
	return applycorev1.ServiceAccount(c.ServiceAccountName, c.Namespace).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentServiceAccountLabel)).
		WithLabels(c.commonLabels(c.Name)).
		WithAnnotations(c.ExtraServiceAccountAnnotations)
}

func (c *Config) ServiceAccount() *applycorev1.ServiceAccountApplyConfiguration {
	sa := applycorev1.ServiceAccount(c.ServiceAccountName, c.Namespace)
	_, _, _ = ApplyPatches(c.unpatchedServiceAccount(), sa, c.Patches, c.Resources)

	// ensure patches don't overwrite anything critical for operator function
	return sa.WithName(c.ServiceAccountName).WithNamespace(c.Namespace).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentServiceAccountLabel)).
		WithOwnerReferences(c.ownerRef())
}

func (c *Config) unpatchedRole() *applyrbacv1.RoleApplyConfiguration {
	return applyrbacv1.Role(c.Name, c.Namespace).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentRoleLabel)).
		WithLabels(c.commonLabels(c.Name)).
		WithRules(
			applyrbacv1.PolicyRule().
				WithAPIGroups("").
				WithResources("endpoints").
				WithVerbs("get", "list", "watch"),
		)
}

func (c *Config) Role() *applyrbacv1.RoleApplyConfiguration {
	role := applyrbacv1.Role(c.Name, c.Namespace)
	_, _, _ = ApplyPatches(c.unpatchedRole(), role, c.Patches, c.Resources)

	// ensure patches don't overwrite anything critical for operator function
	return role.WithName(c.Name).WithNamespace(c.Namespace).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentRoleLabel)).
		WithOwnerReferences(c.ownerRef())
}

func (c *Config) unpatchedRoleBinding() *applyrbacv1.RoleBindingApplyConfiguration {
	return applyrbacv1.RoleBinding(c.Name, c.Namespace).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentRoleBindingLabel)).
		WithLabels(c.commonLabels(c.Name)).
		WithRoleRef(applyrbacv1.RoleRef().
			WithKind("Role").
			WithName(c.Name),
		).WithSubjects(applyrbacv1.Subject().
		WithNamespace(c.Namespace).
		WithKind("ServiceAccount").WithName(c.ServiceAccountName),
	)
}

func (c *Config) RoleBinding() *applyrbacv1.RoleBindingApplyConfiguration {
	rb := applyrbacv1.RoleBinding(c.Name, c.Namespace)
	_, _, _ = ApplyPatches(c.unpatchedRoleBinding(), rb, c.Patches, c.Resources)

	// ensure patches don't overwrite anything critical for operator function
	return rb.WithName(c.Name).WithNamespace(c.Namespace).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentRoleBindingLabel)).
		WithOwnerReferences(c.ownerRef())
}

func (c *Config) unpatchedService() *applycorev1.ServiceApplyConfiguration {
	return applycorev1.Service(c.Name, c.Namespace).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentServiceLabel)).
		WithLabels(c.commonLabels(c.Name)).
		WithSpec(applycorev1.ServiceSpec().
			WithSelector(metadata.LabelsForComponent(c.Name, metadata.ComponentSpiceDBLabelValue)).
			WithPorts(c.servicePorts()...),
		)
}

func (c *Config) Service() *applycorev1.ServiceApplyConfiguration {
	s := applycorev1.Service(c.Name, c.Namespace)
	unpatched := c.unpatchedService()
	_, _, _ = ApplyPatches(unpatched, s, c.Patches, c.Resources)

	// not allowed to patch out the spec
	if s.Spec == nil {
		s.Spec = unpatched.Spec
	}

	// ensure patches don't overwrite anything critical for operator function
	s.WithName(c.Name).WithNamespace(c.Namespace).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentServiceLabel)).
		WithOwnerReferences(c.ownerRef())
	s.Spec.WithSelector(metadata.LabelsForComponent(c.Name, metadata.ComponentSpiceDBLabelValue))
	return s
}

func (c *Config) servicePorts() []*applycorev1.ServicePortApplyConfiguration {
	ports := []*applycorev1.ServicePortApplyConfiguration{
		applycorev1.ServicePort().WithName("grpc").WithPort(50051),
		applycorev1.ServicePort().WithName("gateway").WithPort(8443),
		applycorev1.ServicePort().WithName("metrics").WithPort(9090),
	}
	if c.DispatchEnabled {
		ports = append(ports, applycorev1.ServicePort().WithName("dispatch").WithPort(50053))
	}
	return ports
}

func (c *Config) jobVolumes() []*applycorev1.VolumeApplyConfiguration {
	volumes := make([]*applycorev1.VolumeApplyConfiguration, 0)
	volumes = append(volumes, applycorev1.Volume().WithName(podNameVolume).
		WithDownwardAPI(applycorev1.DownwardAPIVolumeSource().WithItems(
			applycorev1.DownwardAPIVolumeFile().
				WithPath("name").
				WithFieldRef(applycorev1.ObjectFieldSelector().
					WithFieldPath("metadata.name"),
				),
		)))

	if len(c.DatastoreTLSSecretName) > 0 {
		volumes = append(volumes, applycorev1.Volume().WithName(dbTLSVolume).WithSecret(applycorev1.SecretVolumeSource().WithDefaultMode(420).WithSecretName(c.DatastoreTLSSecretName)))
	}
	if len(c.SpannerCredsSecretRef) > 0 {
		volumes = append(volumes, applycorev1.Volume().WithName(spannerVolume).WithSecret(applycorev1.SecretVolumeSource().WithDefaultMode(420).WithSecretName(c.SpannerCredsSecretRef).WithItems(
			applycorev1.KeyToPath().WithKey(spannerCredsFileName).WithPath(spannerCredsFileName),
		)))
	}
	if c.ProjectLabels {
		volumes = append(volumes, applycorev1.Volume().WithName(labelsVolume).
			WithDownwardAPI(applycorev1.DownwardAPIVolumeSource().WithItems(
				applycorev1.DownwardAPIVolumeFile().
					WithPath("labels").
					WithFieldRef(applycorev1.ObjectFieldSelector().
						WithFieldPath("metadata.labels"),
					),
			)))
	}
	if c.ProjectAnnotations {
		volumes = append(volumes, applycorev1.Volume().WithName(annotationsVolume).
			WithDownwardAPI(applycorev1.DownwardAPIVolumeSource().WithItems(
				applycorev1.DownwardAPIVolumeFile().
					WithPath("annotations").
					WithFieldRef(applycorev1.ObjectFieldSelector().
						WithFieldPath("metadata.annotations"),
					),
			)))
	}

	return volumes
}

func (c *Config) jobVolumeMounts() []*applycorev1.VolumeMountApplyConfiguration {
	volumeMounts := make([]*applycorev1.VolumeMountApplyConfiguration, 0)
	if len(c.DatastoreTLSSecretName) > 0 {
		volumeMounts = append(volumeMounts, applycorev1.VolumeMount().WithName(dbTLSVolume).WithMountPath("/spicedb-db-tls").WithReadOnly(true))
	}
	if len(c.SpannerCredsSecretRef) > 0 {
		volumeMounts = append(volumeMounts, applycorev1.VolumeMount().WithName(spannerVolume).WithMountPath(spannerCredsPath).WithReadOnly(true))
	}
	if c.ProjectLabels {
		volumeMounts = append(volumeMounts, applycorev1.VolumeMount().WithName(labelsVolume).WithMountPath("/etc/podlabels"))
	}
	if c.ProjectAnnotations {
		volumeMounts = append(volumeMounts, applycorev1.VolumeMount().WithName(annotationsVolume).WithMountPath("/etc/podannotations"))
	}
	return volumeMounts
}

func (c *Config) jobName(migrationHash string) string {
	size := 15
	if len(migrationHash) < 15 {
		size = len(migrationHash)
	}
	return fmt.Sprintf("%s-migrate-%s", c.Name, migrationHash[:size])
}

func (c *Config) unpatchedMigrationJob(migrationHash string) *applybatchv1.JobApplyConfiguration {
	envPrefix := c.SpiceConfig.EnvPrefix
	envVars := []*applycorev1.EnvVarApplyConfiguration{
		applycorev1.EnvVar().WithName(envPrefix + "_LOG_LEVEL").WithValue(c.MigrationLogLevel),
		applycorev1.EnvVar().WithName(envPrefix + "_DATASTORE_CONN_URI").WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(applycorev1.SecretKeySelector().WithName(c.SecretName).WithKey("datastore_uri"))),
		applycorev1.EnvVar().WithName(envPrefix + "_SECRETS").WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(applycorev1.SecretKeySelector().WithName(c.SecretName).WithKey("migration_secrets").WithOptional(true))),
	}

	keys := make([]string, 0, len(c.Passthrough))
	for k := range c.Passthrough {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		envVars = append(envVars, applycorev1.EnvVar().
			WithName(toEnvVarName(envPrefix, k)).WithValue(c.Passthrough[k]))
	}

	return applybatchv1.Job(c.jobName(migrationHash), c.Namespace).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentMigrationJobLabelValue)).
		WithLabels(c.commonLabels(c.Name)).
		WithAnnotations(map[string]string{
			metadata.SpiceDBMigrationRequirementsKey: migrationHash,
		}).
		WithSpec(applybatchv1.JobSpec().WithTemplate(
			applycorev1.PodTemplateSpec().WithLabels(
				metadata.LabelsForComponent(c.Name, metadata.ComponentMigrationJobLabelValue),
			).
				WithLabels(c.commonLabels(c.Name)).
				WithLabels(c.ExtraPodLabels).
				WithAnnotations(
					c.ExtraPodAnnotations,
				).WithSpec(applycorev1.PodSpec().WithServiceAccountName(c.ServiceAccountName).
				WithContainers(
					applycorev1.Container().
						WithName("migrate").
						WithImage(c.TargetSpiceDBImage).
						WithCommand(c.MigrationConfig.SpiceDBCmd, "migrate", c.MigrationConfig.TargetMigration).
						WithEnv(envVars...).
						WithVolumeMounts(c.jobVolumeMounts()...).
						WithPorts(c.containerPorts()...).
						WithTerminationMessagePolicy(corev1.TerminationMessageFallbackToLogsOnError),
				).WithVolumes(c.jobVolumes()...).WithRestartPolicy(corev1.RestartPolicyOnFailure))))
}

func (c *Config) MigrationJob(migrationHash string) *applybatchv1.JobApplyConfiguration {
	j := applybatchv1.Job(c.jobName(migrationHash), c.Namespace)
	unpatched := c.unpatchedMigrationJob(migrationHash)
	_, _, _ = ApplyPatches(unpatched, j, c.Patches, c.Resources)

	// not allowed to patch out the spec
	if j.Spec == nil {
		j.Spec = unpatched.Spec
	}

	// not allowed to patch out the template
	if j.Spec.Template == nil {
		j.Spec.Template = unpatched.Spec.Template
	}

	// ensure patches don't overwrite anything critical for operator function
	name := c.jobName(migrationHash)
	j.WithName(name).WithNamespace(c.Namespace).WithOwnerReferences(c.ownerRef()).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentMigrationJobLabelValue)).
		WithAnnotations(map[string]string{
			metadata.SpiceDBMigrationRequirementsKey: migrationHash,
		})
	j.Spec.Template.WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentMigrationJobLabelValue))
	return j
}

func (c *Config) containerPorts() []*applycorev1.ContainerPortApplyConfiguration {
	ports := []*applycorev1.ContainerPortApplyConfiguration{
		applycorev1.ContainerPort().WithContainerPort(50051).WithName("grpc"),
		applycorev1.ContainerPort().WithContainerPort(8443).WithName("gateway"),
		applycorev1.ContainerPort().WithContainerPort(9090).WithName("metrics"),
	}
	if c.DispatchEnabled {
		ports = append(ports, applycorev1.ContainerPort().WithContainerPort(50053).WithName("dispatch"))
	}
	return ports
}

func (c *Config) deploymentVolumes() []*applycorev1.VolumeApplyConfiguration {
	volumes := c.jobVolumes()
	// TODO: validate that the secrets exist before we start applying the Deployment
	if len(c.TLSSecretName) > 0 {
		volumes = append(volumes, applycorev1.Volume().WithName(tlsVolume).WithSecret(applycorev1.SecretVolumeSource().WithDefaultMode(420).WithSecretName(c.TLSSecretName)))
	}
	if len(c.DispatchUpstreamCASecretName) > 0 && c.DispatchEnabled {
		volumes = append(volumes, applycorev1.Volume().WithName(dispatchTLSVolume).WithSecret(applycorev1.SecretVolumeSource().WithDefaultMode(420).WithSecretName(c.DispatchUpstreamCASecretName)))
	}
	if len(c.TelemetryTLSCASecretName) > 0 {
		volumes = append(volumes, applycorev1.Volume().WithName(telemetryTLSVolume).WithSecret(applycorev1.SecretVolumeSource().WithDefaultMode(420).WithSecretName(c.TelemetryTLSCASecretName)))
	}
	return volumes
}

func (c *Config) deploymentVolumeMounts() []*applycorev1.VolumeMountApplyConfiguration {
	volumeMounts := c.jobVolumeMounts()
	// TODO: validate that the secrets exist before we start applying the Deployment
	if len(c.TLSSecretName) > 0 {
		volumeMounts = append(volumeMounts, applycorev1.VolumeMount().WithName(tlsVolume).WithMountPath("/tls").WithReadOnly(true))
	}
	if len(c.DispatchUpstreamCASecretName) > 0 && c.DispatchEnabled {
		volumeMounts = append(volumeMounts, applycorev1.VolumeMount().WithName(dispatchTLSVolume).WithMountPath("/dispatch-tls").WithReadOnly(true))
	}
	if len(c.TelemetryTLSCASecretName) > 0 {
		volumeMounts = append(volumeMounts, applycorev1.VolumeMount().WithName(telemetryTLSVolume).WithMountPath("/telemetry-tls").WithReadOnly(true))
	}
	return volumeMounts
}

func (c *Config) probeCmd() []string {
	probeCmd := []string{"grpc_health_probe", "-v", "-addr=localhost:50051"}

	if len(c.TLSSecretName) > 0 {
		probeCmd = append(probeCmd, "-tls", "-tls-no-verify")
	}
	return probeCmd
}

func (c *Config) unpatchedDeployment(migrationHash, secretHash string) *applyappsv1.DeploymentApplyConfiguration {
	if c.SkipMigrations {
		migrationHash = "skipped"
	}
	name := deploymentName(c.Name)
	return applyappsv1.Deployment(name, c.Namespace).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentSpiceDBLabelValue)).
		WithLabels(c.commonLabels(name)).
		WithAnnotations(map[string]string{
			metadata.SpiceDBMigrationRequirementsKey: migrationHash,
		}).
		WithSpec(applyappsv1.DeploymentSpec().
			WithReplicas(c.Replicas).
			WithStrategy(applyappsv1.DeploymentStrategy().
				WithType(appsv1.RollingUpdateDeploymentStrategyType).
				WithRollingUpdate(applyappsv1.RollingUpdateDeployment().WithMaxUnavailable(intstr.FromInt32(0)))).
			WithSelector(applymetav1.LabelSelector().WithMatchLabels(map[string]string{metadata.KubernetesInstanceLabelKey: name})).
			WithTemplate(applycorev1.PodTemplateSpec().
				WithAnnotations(map[string]string{
					metadata.SpiceDBSecretRequirementsKey: secretHash,
					metadata.SpiceDBTargetMigrationKey:    c.MigrationConfig.TargetMigration,
				}).
				WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentSpiceDBLabelValue)).
				WithLabels(c.commonLabels(name)).
				WithLabels(c.ExtraPodLabels).
				WithAnnotations(c.ExtraPodAnnotations).
				WithSpec(applycorev1.PodSpec().WithServiceAccountName(c.ServiceAccountName).WithContainers(
					applycorev1.Container().WithName(ContainerNameSpiceDB).WithImage(c.TargetSpiceDBImage).
						WithCommand(c.SpiceConfig.SpiceDBCmd, "serve").
						WithEnv(c.toEnvVarApplyConfiguration()...).
						WithPorts(c.containerPorts()...).
						WithLivenessProbe(
							applycorev1.Probe().WithExec(applycorev1.ExecAction().WithCommand(c.probeCmd()...)).
								WithInitialDelaySeconds(60).WithFailureThreshold(5).WithPeriodSeconds(10).WithTimeoutSeconds(5),
						).
						WithReadinessProbe(
							applycorev1.Probe().WithExec(applycorev1.ExecAction().WithCommand(c.probeCmd()...)).
								WithFailureThreshold(5).WithPeriodSeconds(10).WithTimeoutSeconds(5),
						).
						WithVolumeMounts(c.deploymentVolumeMounts()...).
						WithTerminationMessagePolicy(corev1.TerminationMessageFallbackToLogsOnError),
				).WithVolumes(c.deploymentVolumes()...))))
}

func (c *Config) Deployment(migrationHash, secretHash string) *applyappsv1.DeploymentApplyConfiguration {
	name := deploymentName(c.Name)
	d := applyappsv1.Deployment(name, c.Namespace)
	unpatched := c.unpatchedDeployment(migrationHash, secretHash)
	_, _, _ = ApplyPatches(unpatched, d, c.Patches, c.Resources)

	// not allowed to patch out the spec
	if d.Spec == nil {
		d.Spec = unpatched.Spec
	}

	// not allowed to patch out the selector
	if d.Spec.Selector == nil {
		d.Spec.Selector = unpatched.Spec.Selector
	}

	// not allowed to patch out the template
	if d.Spec.Template == nil {
		d.Spec.Template = unpatched.Spec.Template
	}

	// ensure patches don't overwrite anything critical for operator function
	d.WithName(name).WithNamespace(c.Namespace).WithOwnerReferences(c.ownerRef()).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentSpiceDBLabelValue)).
		WithAnnotations(map[string]string{
			metadata.SpiceDBMigrationRequirementsKey: migrationHash,
		})
	d.Spec.Selector.WithMatchLabels(map[string]string{metadata.KubernetesInstanceLabelKey: name})
	d.Spec.Template.
		WithAnnotations(map[string]string{
			metadata.SpiceDBSecretRequirementsKey: secretHash,
			metadata.SpiceDBTargetMigrationKey:    c.MigrationConfig.TargetMigration,
		}).
		WithLabels(map[string]string{metadata.KubernetesInstanceLabelKey: name}).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentSpiceDBLabelValue))
	return d
}

func (c *Config) PodDisruptionBudget() *applypolicyv1.PodDisruptionBudgetApplyConfiguration {
	name := pdbName(c.Name)
	d := applypolicyv1.PodDisruptionBudget(name, c.Namespace)
	unpatched := c.unpatchedPDB()
	_, _, _ = ApplyPatches(unpatched, d, c.Patches, c.Resources)

	// ensure patches don't overwrite anything critical for operator function
	d.WithName(name).WithNamespace(c.Namespace).WithOwnerReferences(c.ownerRef()).
		WithLabels(c.commonLabels(name)).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentPDBLabel))
	d.Spec.Selector.WithMatchLabels(map[string]string{metadata.KubernetesInstanceLabelKey: deploymentName(c.Name)})
	return d
}

func (c *Config) unpatchedPDB() *applypolicyv1.PodDisruptionBudgetApplyConfiguration {
	return applypolicyv1.PodDisruptionBudget(pdbName(c.Name), c.Namespace).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentPDBLabel)).
		WithSpec(applypolicyv1.PodDisruptionBudgetSpec().
			WithSelector(applymetav1.LabelSelector().WithMatchLabels(
				map[string]string{metadata.KubernetesInstanceLabelKey: deploymentName(c.Name)},
			)).
			// only allow one pod to be unavailable at a time
			WithMaxUnavailable(intstr.FromInt32(1)),
		)
}

func (c *Config) commonLabels(name string) map[string]string {
	version := ""
	if c.SpiceDBVersion != nil && c.SpiceDBVersion.Name != "" {
		// Dots are valid label characters in Kubernetes labels
		slug.CustomSub = map[string]string{
			".": "__dot__",
		}
		// Use slug to clean the version name (removes spaces, special characters, etc.)
		versionValue := slug.Make(c.SpiceDBVersion.Name)
		// Replace the custom sub with a dot
		versionValue = strings.ReplaceAll(versionValue, "__dot__", ".")

		// Ensure the version is not too long for Kubernetes labels
		if len(versionValue) > 63 {
			versionValue = versionValue[:63]
		}

		if errs := validation.IsValidLabelValue(versionValue); len(errs) == 0 {
			version = versionValue
		}
	}

	cLabels := map[string]string{
		metadata.KubernetesInstanceLabelKey:  name,
		metadata.KubernetesNameLabelKey:      name,
		metadata.KubernetesComponentLabelKey: metadata.ComponentSpiceDBLabelValue,
	}

	if version != "" {
		cLabels[metadata.KubernetesVersionLabelKey] = version
	}

	return cLabels
}

// fixDeploymentPatches modifies any patches that could apply to the deployment
// referencing the old container names and rewrites them to use the new
// stable name
func fixDeploymentPatches(name string, in []v1alpha1.Patch) []v1alpha1.Patch {
	if in == nil {
		return nil
	}
	patches := make([]v1alpha1.Patch, 0, len(in))
	patches = append(patches, in...)
	for i, p := range patches {
		// not a deployment patch
		if p.Kind != "Deployment" && p.Kind != wildcard {
			continue
		}

		// patch doesn't contain the old name
		if !strings.Contains(string(p.Patch), name+"-spicedb") {
			continue
		}

		// determine what kind of patch it is
		decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(p.Patch), 100)
		var json6902op jsonpatch.Operation
		err := decoder.Decode(&json6902op)
		if err != nil {
			continue
		}

		// if there's an operation, it's a json6902 patch, and can't have
		// used the old names
		if json6902op.Kind() != "unknown" {
			continue
		}

		// parse the patch
		smpPatch, err := utilyaml.ToJSON(p.Patch)
		if err != nil {
			continue
		}

		// patch the patch
		patchPatch, err := jsonpatch.DecodePatch([]byte(`[
			{"op": "replace", "path": "/spec/template/spec/containers/0/name", "value": "spicedb"}
		]`))
		if err != nil {
			continue
		}
		modified, err := patchPatch.Apply(smpPatch)
		if err != nil {
			continue
		}

		// update the stored patch
		patches[i].Patch = modified
	}
	return patches
}

// toEnvVarName converts a key from the api object into an env var name.
// the key isCamelCased will be converted to PREFIX_IS_CAMEL_CASED
func toEnvVarName(prefix string, key string) string {
	prefix = strings.TrimSuffix(prefix, "_")
	envVarParts := []string{strings.ToUpper(prefix)}
	for _, p := range camelcase.Split(key) {
		envVarParts = append(envVarParts, strings.ToUpper(p))
	}
	return strings.Join(envVarParts, "_")
}

// deploymentName returns the name of the unpatchedDeployment given a SpiceDBCluster name
func deploymentName(name string) string {
	return fmt.Sprintf("%s-spicedb", name)
}

// pdbName returns the name of the unpatchedPDB given a SpiceDBCluster name
func pdbName(name string) string {
	return fmt.Sprintf("%s-spicedb", name)
}
