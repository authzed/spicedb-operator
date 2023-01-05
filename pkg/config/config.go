package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fatih/camelcase"
	"github.com/jzelinskie/stringz"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	applybatchv1 "k8s.io/client-go/applyconfigurations/batch/v1"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	applymetav1 "k8s.io/client-go/applyconfigurations/meta/v1"
	applyrbacv1 "k8s.io/client-go/applyconfigurations/rbac/v1"

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

	DefaultTLSKeyFile = "/tls/tls.key"
	DefaultTLSCrtFile = "/tls/tls.crt"
)

type key[V comparable] struct {
	key          string
	defaultValue V
}

var (
	imageKey                          = newStringKey("image")
	tlsSecretNameKey                  = newStringKey("tlsSecretName")
	dispatchCAKey                     = newStringKey("dispatchUpstreamCASecretName")
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
	replicasKey                       = newIntOrStringKey("replicas", 2)
	replicasKeyForMemory              = newIntOrStringKey("replicas", 1)
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
	vs, ok := v.(string)
	if !ok {
		return ""
	}
	return vs
}

// Config holds all values required to create and manage a cluster.
// Note: The config object holds values from referenced secrets for
// hashing purposes; these should not be used directly (instead the secret
// should be mounted)
type Config struct {
	MigrationConfig
	SpiceConfig
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
	DispatchUpstreamCASecretName   string
	TelemetryTLSCASecretName       string
	SecretName                     string
	ExtraPodLabels                 map[string]string
	ExtraPodAnnotations            map[string]string
	ExtraServiceAccountAnnotations map[string]string
	ServiceAccountName             string
	Passthrough                    map[string]string
}

// NewConfig checks that the values in the config + the secret are sane
func NewConfig(nn types.NamespacedName, uid types.UID, version, channel string, currentVersion *v1alpha1.SpiceDBVersion, globalConfig *OperatorConfig, rawConfig json.RawMessage, secret *corev1.Secret, rolling bool) (*Config, Warning, error) {
	config := RawConfig(make(map[string]any))
	if err := json.Unmarshal(rawConfig, &config); err != nil {
		return nil, nil, fmt.Errorf("couldn't parse config: %w", err)
	}

	passthroughConfig := make(map[string]string, 0)
	errs := make([]error, 0)
	warnings := make([]error, 0)
	spiceConfig := SpiceConfig{
		Name:                         nn.Name,
		Namespace:                    nn.Namespace,
		UID:                          string(uid),
		TLSSecretName:                tlsSecretNameKey.pop(config),
		DispatchUpstreamCASecretName: dispatchCAKey.pop(config),
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

	// if there's a required edge from the current image, that edge is taken
	// unless the current config is equal to the input.
	image := imageKey.pop(config)

	baseImage, targetSpiceDBVersion, state, err := globalConfig.ComputeTarget(globalConfig.ImageName, image, version, channel, datastoreEngine, currentVersion, rolling)
	if err != nil {
		errs = append(errs, err)
	}

	migrationConfig.SpiceDBVersion = targetSpiceDBVersion
	migrationConfig.TargetPhase = state.Phase
	migrationConfig.TargetMigration = state.Migration
	if len(migrationConfig.TargetMigration) == 0 {
		migrationConfig.TargetMigration = Head
	}

	switch {
	case len(state.Digest) > 0:
		migrationConfig.TargetSpiceDBImage = baseImage + "@" + state.Digest
	case len(state.Tag) > 0:
		migrationConfig.TargetSpiceDBImage = baseImage + ":" + state.Tag
	default:
		errs = append(errs, fmt.Errorf("no update found in channel"))
	}

	migrationConfig.DatastoreEngine = datastoreEngine
	passthroughConfig["datastoreEngine"] = datastoreEngine
	passthroughConfig["dispatchClusterEnabled"] = strconv.FormatBool(datastoreEngine != "memory")

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

	selectedReplicaKey := replicasKey
	if datastoreEngine == "memory" {
		selectedReplicaKey = replicasKeyForMemory
	}

	replicas, err := selectedReplicaKey.pop(config)
	if err != nil {
		errs = append(errs, fmt.Errorf("invalid value for replicas %q: %w", replicas, err))
	}

	spiceConfig.Replicas = int32(replicas)
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

	spiceConfig.ServiceAccountName = serviceAccountNameKey.pop(config)

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

	if len(spiceConfig.DispatchUpstreamCASecretName) > 0 {
		passthroughConfig["dispatchUpstreamCAPath"] = "/dispatch-tls/tls.crt"
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

	spiceConfig.Passthrough = passthroughConfig

	warning := Warning(errors.NewAggregate(warnings))
	if len(errs) > 0 {
		return nil, warning, errors.NewAggregate(errs)
	}

	return &Config{
		MigrationConfig: migrationConfig,
		SpiceConfig:     spiceConfig,
	}, warning, nil
}

// ToEnvVarApplyConfiguration returns a set of env variables to apply to a
// spicedb container
func (c *Config) ToEnvVarApplyConfiguration() []*applycorev1.EnvVarApplyConfiguration {
	// Set non-passthrough config that is either generated directly by the
	// controller (dispatch address), has some direct effect on the cluster
	// (tls), or lives in an external secret (preshared key).
	envVars := []*applycorev1.EnvVarApplyConfiguration{
		applycorev1.EnvVar().WithName(c.SpiceConfig.EnvPrefix + "_LOG_LEVEL").WithValue(c.LogLevel),
		applycorev1.EnvVar().WithName(c.SpiceConfig.EnvPrefix + "_GRPC_PRESHARED_KEY").
			WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(
				applycorev1.SecretKeySelector().WithName(c.SecretName).WithKey("preshared_key"))),
	}
	if c.DatastoreEngine != "memory" {
		envVars = append(envVars,
			applycorev1.EnvVar().WithName(c.SpiceConfig.EnvPrefix+"_DATASTORE_CONN_URI").WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(
				applycorev1.SecretKeySelector().WithName(c.SecretName).WithKey("datastore_uri"))),
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
			WithName(ToEnvVarName(c.SpiceConfig.EnvPrefix, k)).WithValue(c.Passthrough[k]))
	}

	return envVars
}

func (c *Config) OwnerRef() *applymetav1.OwnerReferenceApplyConfiguration {
	return applymetav1.OwnerReference().
		WithName(c.Name).
		WithKind(v1alpha1.SpiceDBClusterKind).
		WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
		WithUID(types.UID(c.UID))
}

func (c *Config) ServiceAccount() *applycorev1.ServiceAccountApplyConfiguration {
	name := stringz.DefaultEmpty(c.ServiceAccountName, c.Name)
	return applycorev1.ServiceAccount(name, c.Namespace).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentServiceAccountLabel)).
		WithAnnotations(c.ExtraServiceAccountAnnotations).
		WithOwnerReferences(c.OwnerRef())
}

func (c *Config) Role() *applyrbacv1.RoleApplyConfiguration {
	return applyrbacv1.Role(c.Name, c.Namespace).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentRoleLabel)).
		WithOwnerReferences(c.OwnerRef()).
		WithRules(
			applyrbacv1.PolicyRule().
				WithAPIGroups("").
				WithResources("endpoints").
				WithVerbs("get", "list", "watch"),
		)
}

func (c *Config) RoleBinding() *applyrbacv1.RoleBindingApplyConfiguration {
	return applyrbacv1.RoleBinding(c.Name, c.Namespace).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentRoleBindingLabel)).
		WithOwnerReferences(c.OwnerRef()).
		WithRoleRef(applyrbacv1.RoleRef().
			WithKind("Role").
			WithName(c.Name),
		).WithSubjects(applyrbacv1.Subject().
		WithNamespace(c.Namespace).
		WithKind("ServiceAccount").WithName(c.Name),
	)
}

func (c *Config) Service() *applycorev1.ServiceApplyConfiguration {
	return applycorev1.Service(c.Name, c.Namespace).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentServiceLabel)).
		WithOwnerReferences(c.OwnerRef()).
		WithSpec(applycorev1.ServiceSpec().
			WithSelector(metadata.LabelsForComponent(c.Name, metadata.ComponentSpiceDBLabelValue)).
			WithPorts(c.servicePorts()...),
		)
}

func (c *Config) servicePorts() []*applycorev1.ServicePortApplyConfiguration {
	ports := []*applycorev1.ServicePortApplyConfiguration{
		applycorev1.ServicePort().WithName("grpc").WithPort(50051),
		applycorev1.ServicePort().WithName("gateway").WithPort(8443),
		applycorev1.ServicePort().WithName("metrics").WithPort(9090),
	}
	if c.DatastoreEngine != "memory" {
		ports = append(ports, applycorev1.ServicePort().WithName("dispatch").WithPort(50053))
	}
	return ports
}

func (c *Config) jobVolumes() []*applycorev1.VolumeApplyConfiguration {
	volumes := make([]*applycorev1.VolumeApplyConfiguration, 0)
	if len(c.DatastoreTLSSecretName) > 0 {
		volumes = append(volumes, applycorev1.Volume().WithName(dbTLSVolume).WithSecret(applycorev1.SecretVolumeSource().WithDefaultMode(420).WithSecretName(c.DatastoreTLSSecretName)))
	}
	if len(c.SpannerCredsSecretRef) > 0 {
		volumes = append(volumes, applycorev1.Volume().WithName(spannerVolume).WithSecret(applycorev1.SecretVolumeSource().WithDefaultMode(420).WithSecretName(c.SpannerCredsSecretRef).WithItems(
			applycorev1.KeyToPath().WithKey("credentials.json").WithPath("credentials.json"),
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
		volumeMounts = append(volumeMounts, applycorev1.VolumeMount().WithName(spannerVolume).WithMountPath("/spanner-credentials").WithReadOnly(true))
	}
	return volumeMounts
}

func (c *Config) MigrationJob(migrationHash string) *applybatchv1.JobApplyConfiguration {
	name := fmt.Sprintf("%s-migrate-%s", c.Name, migrationHash[:15])
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
			WithName(ToEnvVarName(envPrefix, k)).WithValue(c.Passthrough[k]))
	}

	return applybatchv1.Job(name, c.Namespace).
		WithOwnerReferences(c.OwnerRef()).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentMigrationJobLabelValue)).
		WithAnnotations(map[string]string{
			metadata.SpiceDBMigrationRequirementsKey: migrationHash,
		}).
		WithSpec(applybatchv1.JobSpec().WithTemplate(
			applycorev1.PodTemplateSpec().WithLabels(
				metadata.LabelsForComponent(c.Name, metadata.ComponentMigrationJobLabelValue),
			).WithSpec(applycorev1.PodSpec().WithServiceAccountName(c.Name).
				WithContainers(
					applycorev1.Container().
						WithName(name).
						WithImage(c.TargetSpiceDBImage).
						WithCommand(c.MigrationConfig.SpiceDBCmd, "migrate", c.MigrationConfig.TargetMigration).
						WithEnv(envVars...).
						WithVolumeMounts(c.jobVolumeMounts()...).
						WithPorts(c.containerPorts()...).
						WithTerminationMessagePolicy(corev1.TerminationMessageFallbackToLogsOnError),
				).WithVolumes(c.jobVolumes()...).WithRestartPolicy(corev1.RestartPolicyNever))))
}

func (c *Config) containerPorts() []*applycorev1.ContainerPortApplyConfiguration {
	ports := []*applycorev1.ContainerPortApplyConfiguration{
		applycorev1.ContainerPort().WithContainerPort(50051).WithName("grpc"),
		applycorev1.ContainerPort().WithContainerPort(8443).WithName("gateway"),
		applycorev1.ContainerPort().WithContainerPort(9090).WithName("metrics"),
	}
	if c.DatastoreEngine != "memory" {
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
	if len(c.DispatchUpstreamCASecretName) > 0 {
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
	if len(c.DispatchUpstreamCASecretName) > 0 {
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

func (c *Config) Deployment(migrationHash, secretHash string) *applyappsv1.DeploymentApplyConfiguration {
	if c.SkipMigrations {
		migrationHash = "skipped"
	}
	name := DeploymentName(c.Name)
	return applyappsv1.Deployment(name, c.Namespace).WithOwnerReferences(c.OwnerRef()).
		WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentSpiceDBLabelValue)).
		WithAnnotations(map[string]string{
			metadata.SpiceDBMigrationRequirementsKey: migrationHash,
		}).
		WithSpec(applyappsv1.DeploymentSpec().
			WithReplicas(c.Replicas).
			WithStrategy(applyappsv1.DeploymentStrategy().
				WithType(appsv1.RollingUpdateDeploymentStrategyType).
				WithRollingUpdate(applyappsv1.RollingUpdateDeployment().WithMaxUnavailable(intstr.FromInt(0)))).
			WithSelector(applymetav1.LabelSelector().WithMatchLabels(map[string]string{"app.kubernetes.io/instance": name})).
			WithTemplate(applycorev1.PodTemplateSpec().
				WithAnnotations(map[string]string{
					metadata.SpiceDBSecretRequirementsKey: secretHash,
					metadata.SpiceDBTargetMigrationKey:    c.MigrationConfig.TargetMigration,
				}).
				WithLabels(map[string]string{"app.kubernetes.io/instance": name}).
				WithLabels(metadata.LabelsForComponent(c.Name, metadata.ComponentSpiceDBLabelValue)).
				WithLabels(c.ExtraPodLabels).
				WithAnnotations(c.ExtraPodAnnotations).
				WithSpec(applycorev1.PodSpec().WithServiceAccountName(c.Name).WithContainers(
					applycorev1.Container().WithName(name).WithImage(c.TargetSpiceDBImage).
						WithCommand(c.SpiceConfig.SpiceDBCmd, "serve").
						WithEnv(c.ToEnvVarApplyConfiguration()...).
						WithPorts(c.containerPorts()...).
						WithLivenessProbe(
							applycorev1.Probe().WithExec(applycorev1.ExecAction().WithCommand(c.probeCmd()...)).
								WithInitialDelaySeconds(60).WithFailureThreshold(5).WithPeriodSeconds(10).WithTimeoutSeconds(5),
						).WithReadinessProbe(
						applycorev1.Probe().WithExec(applycorev1.ExecAction().WithCommand(c.probeCmd()...)).
							WithFailureThreshold(5).WithPeriodSeconds(10).WithTimeoutSeconds(5),
					).WithVolumeMounts(c.deploymentVolumeMounts()...),
				).WithVolumes(c.deploymentVolumes()...))))
}

// ToEnvVarName converts a key from the api object into an env var name.
// the key isCamelCased will be converted to PREFIX_IS_CAMEL_CASED
func ToEnvVarName(prefix string, key string) string {
	prefix = strings.TrimSuffix(prefix, "_")
	envVarParts := []string{strings.ToUpper(prefix)}
	for _, p := range camelcase.Split(key) {
		envVarParts = append(envVarParts, strings.ToUpper(p))
	}
	return strings.Join(envVarParts, "_")
}

// DeploymentName returns the name of the deployment given a SpiceDBCluster name
func DeploymentName(name string) string {
	return fmt.Sprintf("%s-spicedb", name)
}
