package cluster

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	applybatchv1 "k8s.io/client-go/applyconfigurations/batch/v1"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	applymetav1 "k8s.io/client-go/applyconfigurations/meta/v1"
	applyrbacv1 "k8s.io/client-go/applyconfigurations/rbac/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/util"
)

const (
	OwnerLabelKey                   = "authzed.com/cluster"
	ComponentLabelKey               = "authzed.com/cluster-component"
	ComponentSpiceDBLabelValue      = "spicedb"
	ComponentMigrationJobLabelValue = "migration-job"
	ComponentServiceAccountLabel    = "spicedb-serviceaccount"
	ComponentRoleLabel              = "spicedb-role"
	ComponentServiceLabel           = "spicedb-service"
	ComponentRoleBindingLabel       = "spicedb-rolebinding"
	SpiceDBMigrationRequirementsKey = "authzed.com/spicedb-migration"
	SpiceDBConfigKey                = "authzed.com/spicedb-configuration"
)

var (
	v1alpha1ClusterGVR = v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.AuthzedEnterpriseClusterResourceName)
	authzedClusterGR   = v1alpha1ClusterGVR.GroupResource()

	forceOwned = metav1.ApplyOptions{FieldManager: "authzed-operator", Force: true}
)

type Reconciler struct {
	done      func() func()
	requeue   func(duration time.Duration) func()
	cluster   *v1alpha1.AuthzedEnterpriseCluster
	client    dynamic.Interface
	kclient   kubernetes.Interface
	informers map[schema.GroupVersionResource]dynamicinformer.DynamicSharedInformerFactory
	recorder  record.EventRecorder

	spiceDBImage string
}

// syncCluster inspects the current AuthzedEnterpriseCluster object and ensures
// the desired state is persisted on the cluster.
// `cluster` is a copy of the object from the cache and is safe to mutate.
func (r *Reconciler) sync(ctx context.Context) func() {
	klog.V(4).Infof("syncing cluster %s/%s", r.cluster.Namespace, r.cluster.Name)

	// paused objects are not watched, but may be in the queue due to a sync
	// of a dependent object
	if util.IsPaused(r.cluster) {
		return r.pause(ctx)
	}

	// TODO: generate default secret if missing
	// get the secret and see if it's changed
	secret, secretHash, next := r.getOrAdoptSecret(ctx, r.cluster.NamespacedName(), r.cluster.Spec.SecretRef)
	if next != nil {
		return next
	}

	if r.cluster.ObjectMeta.Generation != r.cluster.Status.ObservedGeneration || secretHash != r.cluster.Status.SecretHash {
		patch := v1alpha1.NewPatch(r.cluster.NamespacedName(), r.cluster.Generation)
		patch.Status.ObservedGeneration = r.cluster.ObjectMeta.Generation
		patch.Status.SecretHash = secretHash
		meta.SetStatusCondition(&patch.Status.Conditions, v1alpha1.NewValidatingConfigCondition(secretHash))
		if err := r.PatchStatus(ctx, patch); err != nil {
			return r.requeue(0)
		}
	}

	config, err := r.validConfig(r.cluster.Spec.Config, secret)
	if err != nil {
		patch := v1alpha1.NewPatch(r.cluster.NamespacedName(), r.cluster.Generation)
		meta.SetStatusCondition(&patch.Status.Conditions, v1alpha1.NewInvalidConfigCondition(secretHash, err))
		if err := r.PatchStatus(ctx, patch); err != nil {
			return r.requeue(0)
		}
		// if the config is invalid, there's no work to do until it has changed
		return r.done()
	}

	// config is valid

	// TODO: parallel
	// ensure rbac is ready
	if next := r.ensureRBAC(ctx); next != nil {
		return next
	}

	// ensure service
	if next := r.ensureService(ctx, r.cluster.NamespacedName()); next != nil {
		return next
	}

	spiceDBDeployments, err := GetComponent[*appsv1.Deployment](r.informers, appsv1.SchemeGroupVersion.WithResource("deployments"), r.cluster.NamespacedName(), ComponentSpiceDBLabelValue)
	if err != nil {
		r.requeue(0)
	}

	if len(spiceDBDeployments) > 1 {
		// TODO delete deployments without matching hashes
	}

	var spiceDBDeployment *appsv1.Deployment
	if len(spiceDBDeployments) > 0 {
		spiceDBDeployment = spiceDBDeployments[0]
	}

	// check migration level if
	// 	- there's no deployment
	//  - there's been a relevant config change (datastore uri, engine, credentials, target spicedb version)

	// determine if config / state has changed in some way that would require us to check
	// whether the datastore is fully migrated
	checkMigrations, next := r.migrationCheckRequired(spiceDBDeployment, &config.MigrationConfig)
	if next != nil {
		return next
	}

	// fetch the migration job (if any)
	migrationJobs, err := GetComponent[*batchv1.Job](r.informers, batchv1.SchemeGroupVersion.WithResource("jobs"), r.cluster.NamespacedName(), ComponentMigrationJobLabelValue)
	if err != nil {
		return r.requeue(0)
	}

	// there can be multiple migration jobs if the config was changed before
	// older jobs were cleaned up
	if len(migrationJobs) > 1 {
		// TODO: migration hash is calculated in many places
		migrationHash, err := util.SecureHashObject(config)
		if err != nil {
			return r.requeue(0)
		}

		// delete jobs without matching hashes
		for _, j := range migrationJobs {
			shouldDelete := func() bool {
				annotations := j.Annotations
				if annotations == nil {
					return true
				}
				hash, ok := annotations[SpiceDBMigrationRequirementsKey]
				if !ok {
					return true
				}
				return subtle.ConstantTimeCompare([]byte(hash), []byte(migrationHash)) != 1
			}
			if !shouldDelete() {
				continue
			}
			if err := r.kclient.BatchV1().Jobs(j.GetNamespace()).Delete(ctx, j.GetName(), metav1.DeleteOptions{}); err != nil {
				utilruntime.HandleError(err)
				return r.requeue(0)
			}
		}
		return r.requeue(1 * time.Second)
	}
	var migrationJob *batchv1.Job
	if len(migrationJobs) > 0 {
		migrationJob = migrationJobs[0]
	}

	if checkMigrations {
		patch := v1alpha1.NewPatch(r.cluster.NamespacedName(), r.cluster.Generation)
		meta.SetStatusCondition(&patch.Status.Conditions, v1alpha1.NewVerifyingMigrationCondition())
		if err := r.PatchStatus(ctx, patch); err != nil {
			return r.requeue(0)
		}

		migrationsRequired, err := r.checkDatastoreMigrationLevel(spiceDBDeployment, &config.MigrationConfig)
		if err != nil {
			return r.requeue(0)
		}
		if migrationsRequired {
			patch := v1alpha1.NewPatch(r.cluster.NamespacedName(), r.cluster.Generation)
			meta.SetStatusCondition(&patch.Status.Conditions, v1alpha1.NewMigratingCondition(config.DatastoreEngine, "head"))
			if err := r.PatchStatus(ctx, patch); err != nil {
				return r.requeue(0)
			}

			// actually migrate
			next := r.migrateHead(ctx, r.cluster, migrationJob, &config.MigrationConfig)
			if next != nil {
				return next
			}
		}
	}

	// datastore is up and fully migrated

	// ensure deployment exists with proper config
	deploymentUpdateRequired, err := r.deploymentUpdateRequired(spiceDBDeployment, &config.SpiceConfig, &config.MigrationConfig)
	if err != nil {
		return r.requeue(0)
	}

	if deploymentUpdateRequired {
		if err := r.updateDeployment(ctx, r.cluster.NamespacedName(), &config.SpiceConfig, &config.MigrationConfig); err != nil {
			return r.requeue(0)
		}
	}

	// check if we need to delete the migration job (deployment exists with the same migration hash)
	if migrationJob != nil && spiceDBDeployment != nil && migrationJob.Annotations[SpiceDBMigrationRequirementsKey] == spiceDBDeployment.Annotations[SpiceDBMigrationRequirementsKey] {
		if err := r.kclient.BatchV1().Jobs(r.cluster.Namespace).Delete(ctx, migrationJob.GetName(), metav1.DeleteOptions{}); err != nil {
			utilruntime.HandleError(err)
			return r.requeue(0)
		}
	}

	return r.done()
}

// pause sets the pause condition in the status in response to a user adding
// the pause label to the resource
func (r *Reconciler) pause(ctx context.Context) func() {
	if meta.FindStatusCondition(r.cluster.Status.Conditions, v1alpha1.ConditionTypePaused) != nil {
		return r.done()
	}
	patch := v1alpha1.NewPatch(r.cluster.NamespacedName(), r.cluster.Generation)
	meta.SetStatusCondition(&patch.Status.Conditions, v1alpha1.NewPausedCondition())
	if err := r.PatchStatus(ctx, patch); err != nil {
		return r.requeue(0)
	}
	return r.done()
}

// selfPause sets the pause condition in the status and sets the label
// controller-paused. The controller chooses to pause reconciliation in some
// cases that it believes user intervention is required.
func (r *Reconciler) selfPause(ctx context.Context, patch *v1alpha1.AuthzedEnterpriseCluster) func() {
	meta.SetStatusCondition(&patch.Status.Conditions, v1alpha1.NewSelfPausedCondition())
	if err := r.PatchStatus(ctx, patch); err != nil {
		return r.requeue(0)
	}
	patch.ObjectMeta.Labels = map[string]string{
		util.PausedControllerSelectorKey: string(r.cluster.UID),
	}
	if err := r.Patch(ctx, patch); err != nil {
		return r.requeue(0)
	}
	return r.done()
}

// getOrAdoptSecret will return the secret from the cache, or label the secret
// so that it will be in the cache in the future (if labelling succeeds, the
// newly labelled secret will be returned). Returns nil if cluster has no
// secretRef.
func (r *Reconciler) getOrAdoptSecret(ctx context.Context, nn types.NamespacedName, secretRef string) (secret *corev1.Secret, secretHash string, next func()) {
	if secretRef == "" {
		return
	}
	secretsGVR := corev1.SchemeGroupVersion.WithResource("secrets")
	secrets, err := r.informers[secretsGVR].ForResource(secretsGVR).Informer().GetIndexer().ByIndex(OwningClusterIndex, nn.String())
	if err != nil {
		next = r.requeue(0)
		return
	}
	switch len(secrets) {
	case 0:
		// secret is not in cache, which means it's not labelled for the cluster
		// fetch it and add the label to it.
		secret, err = r.kclient.CoreV1().Secrets(nn.Namespace).Apply(ctx, applycorev1.Secret(secretRef, nn.Namespace).WithLabels(map[string]string{
			OwnerLabelKey: nn.Name,
		}), forceOwned)
		// TODO: events
		// r.recorder.Event(secret, "Adopted", "ReferencedByCluster", "Secret was referenced as the secret source for an AuthzedEnterpriseCluster; it has been labelled to mark it as part of the configuration for that cluster.")
	case 1:
		var ok bool
		secret, ok = secrets[0].(*corev1.Secret)
		if !ok {
			err = fmt.Errorf("non-secret object found in secret informer cache for %s/%s; should not be possible", nn.Namespace, secretRef)
		}
	default:
		err = fmt.Errorf("more than one secret found for %s/%s; should not be possible", nn.Namespace, secretRef)
	}
	if err != nil {
		utilruntime.HandleError(err)
		next = r.requeue(0)
		return
	}
	secretHash, err = util.SecureHashObject(secret)
	if err != nil {
		utilruntime.HandleError(err)
		next = r.requeue(0)
		return
	}
	return secret, secretHash, nil
}

// validConfig checks that the values in the config + the secret are sane
func (r *Reconciler) validConfig(config map[string]string, secret *corev1.Secret) (*Config, error) {
	datastoreEngine, ok := config["datastore_engine"]
	if !ok {
		return nil, fmt.Errorf("datastore_engine is a required field")
	}

	datastoreURI, ok := secret.Data["datastore_uri"]
	if !ok {
		return nil, fmt.Errorf("secret must contain a datastore-uri field")
	}

	psk, ok := secret.Data["preshared_key"]
	if !ok {
		return nil, fmt.Errorf("secret must contain a preshared_key field")
	}

	replicasVal, ok := config["replicas"]
	if !ok {
		replicasVal = "2"
	}
	replicas, err := strconv.ParseInt(replicasVal, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("invalid value for replicas %q: %w", replicas, err)
	}

	envPrefix, ok := config["env_prefix"]
	if !ok {
		envPrefix = "SPICEDB_"
	}

	spicedbCmd, ok := config["cmd"]
	if !ok {
		spicedbCmd = "spicedb"
	}

	prefixesRequired, ok := config["prefixes_required"]
	if !ok {
		prefixesRequired = "true"
	}

	overlapStrategy, ok := config["overlap_strategy"]
	if !ok {
		overlapStrategy = "prefix"
	}

	// TODO: should we even store refs to the values from the secret?
	return &Config{MigrationConfig: MigrationConfig{
		DatastoreEngine:        datastoreEngine,
		DatastoreURI:           string(datastoreURI),
		SpannerCredsSecretRef:  config["spanner_credentials"],
		TargetSpiceDBImage:     r.spiceDBImage,
		EnvPrefix:              envPrefix,
		SpiceDBCmd:             spicedbCmd,
		DatastoreTLSSecretName: config["datastore_tls_secret_name"],
	}, SpiceConfig: SpiceConfig{
		Replicas:         int32(replicas),
		PresharedKey:     string(psk),
		EnvPrefix:        envPrefix,
		SpiceDBCmd:       spicedbCmd,
		TLSSecretName:    config["tls_secret_name"],
		PrefixesRequired: prefixesRequired == "true",
		OverlapStrategy:  overlapStrategy,
	}}, nil
}

// migrationCheckRequired returns true if the current state requires that we
// check the migration level of the database. This can be required if there
// has been a relevant config change (database-uri, for example) or if the
// cluster is initializing.
func (r *Reconciler) migrationCheckRequired(spiceDBDeployment *appsv1.Deployment, config *MigrationConfig) (bool, func()) {
	if spiceDBDeployment == nil {
		return true, nil
	}

	// check the migration requirements hash
	migrationHash, err := util.SecureHashObject(config)
	if err != nil {
		return false, r.requeue(0)
	}

	// no annotations means we need to check
	annotations := spiceDBDeployment.Annotations
	if annotations == nil {
		return true, nil
	}

	// if hash is different, migration check is required
	return subtle.ConstantTimeCompare([]byte(annotations[SpiceDBMigrationRequirementsKey]), []byte(migrationHash)) != 1, nil
}

func (r *Reconciler) checkDatastoreMigrationLevel(spiceDBDeployment *appsv1.Deployment, config *MigrationConfig) (bool, error) {
	// if there is no deployment, we need to migrate
	if spiceDBDeployment == nil {
		return true, nil
	}

	// we're deploying a new version and should check for migrations first
	if spiceDBDeployment.Spec.Template.Spec.Containers[0].Image != config.TargetSpiceDBImage {
		return true, nil
	}

	return false, nil
}

func (r *Reconciler) migrateHead(ctx context.Context, cluster *v1alpha1.AuthzedEnterpriseCluster, migrationJob *batchv1.Job, migrationConfig *MigrationConfig) func() {
	migrationJobRequired, err := r.migrationJobApplyRequired(migrationJob, migrationConfig)
	if err != nil {
		utilruntime.HandleError(err)
		return r.requeue(0)
	}
	if migrationJobRequired {
		// TODO: this is calculated in the step above too
		migrationConfigHash, err := util.SecureHashObject(migrationConfig)
		if err != nil {
			utilruntime.HandleError(err)
			return r.requeue(0)
		}
		if err := r.updateJob(ctx, cluster.NamespacedName(), migrationConfigHash, migrationConfig); err != nil {
			utilruntime.HandleError(err)
			return r.requeue(0)
		}
		// TODO: this would be a good place to grab the updated job's revision
		//  and wait on future resyncs
		// job has been created, requeue and check its status
		return r.requeue(1 * time.Second)
	}

	// if migration failed entirely, pause so we can diagnose
	if c := findJobCondition(migrationJob, batchv1.JobFailed); c != nil {
		err := fmt.Errorf("migration job failed: %s", c.Message)
		utilruntime.HandleError(err)
		patch := v1alpha1.NewPatch(r.cluster.NamespacedName(), r.cluster.Generation)
		meta.SetStatusCondition(&patch.Status.Conditions, v1alpha1.NewMigrationFailedCondition(migrationConfig.DatastoreEngine, "head", err))
		if err := r.PatchStatus(ctx, patch); err != nil {
			return r.requeue(0)
		}
		if err := r.selfPause(ctx, patch); err != nil {
			return r.requeue(0)
		}
		return r.done()
	}

	// if done, go to the next step
	if findJobCondition(migrationJob, batchv1.JobComplete) != nil {
		return nil
	}

	// otherwise, it's created but still running, just wait
	return r.requeue(5 * time.Second)
}

// deploymentUpdateRequired returns true if the deployment on the cluster needs
// to be updated to match the current config
func (r *Reconciler) migrationJobApplyRequired(migrationJob *batchv1.Job, migrationConfig *MigrationConfig) (bool, error) {
	// if there's no spice job, need to create one
	if migrationJob == nil {
		return true, nil
	}

	// calculate the config hash
	migrationConfigHash, err := util.SecureHashObject(migrationConfig)
	if err != nil {
		return false, err
	}

	// no annotations means we need to update
	annotations := migrationJob.Annotations
	if annotations == nil {
		return true, nil
	}

	// if hash is different, update is required
	return subtle.ConstantTimeCompare([]byte(annotations[SpiceDBMigrationRequirementsKey]), []byte(migrationConfigHash)) != 1, nil
}

// deploymentUpdateRequired returns true if the deployment on the cluster needs
// to be updated to match the current config
func (r *Reconciler) deploymentUpdateRequired(spiceDBDeployment *appsv1.Deployment, config *SpiceConfig, migrateConfig *MigrationConfig) (bool, error) {
	// if there's no spice deployment, need to create one
	if spiceDBDeployment == nil {
		return true, nil
	}

	// calculate the config hash
	spiceDBConfigHash, err := util.SecureHashObject(config)
	if err != nil {
		return false, err
	}
	migrationHash, err := util.SecureHashObject(migrateConfig)
	if err != nil {
		return false, err
	}

	// no annotations means we need to update
	annotations := spiceDBDeployment.Annotations
	if annotations == nil {
		return true, nil
	}

	// if hash is different, update is required
	return annotations[SpiceDBConfigKey] != spiceDBConfigHash ||
		subtle.ConstantTimeCompare([]byte(annotations[SpiceDBMigrationRequirementsKey]), []byte(migrationHash)) != 1, nil
}

func (r *Reconciler) updateDeployment(ctx context.Context, nn types.NamespacedName, config *SpiceConfig, migrateConfig *MigrationConfig) error {
	// calculate the config hashes
	spiceDBConfigHash, err := util.SecureHashObject(config)
	if err != nil {
		utilruntime.HandleError(err)
		return err
	}
	migrationHash, err := util.SecureHashObject(migrateConfig)
	if err != nil {
		utilruntime.HandleError(err)
		return err
	}

	name := fmt.Sprintf("%s-spicedb", nn.Name)
	dep := applyappsv1.Deployment(name, nn.Namespace)
	dep = dep.WithLabels(LabelsForComponent(nn.Name, ComponentSpiceDBLabelValue))
	dep = dep.WithAnnotations(map[string]string{
		SpiceDBMigrationRequirementsKey: migrationHash,
		SpiceDBConfigKey:                spiceDBConfigHash,
	})
	dep = dep.WithOwnerReferences(applymetav1.OwnerReference().
		WithName(nn.Name).
		WithKind(v1alpha1.AuthzedEnterpriseClusterKind).
		WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
		WithUID(r.cluster.UID))

	depEnv := []*applycorev1.EnvVarApplyConfiguration{
		applycorev1.EnvVar().WithName(config.EnvPrefix + "_LOG_LEVEL").WithValue(config.LogLevel),
		applycorev1.EnvVar().WithName(config.EnvPrefix + "_DATASTORE_ENGINE").WithValue(migrateConfig.DatastoreEngine),
		applycorev1.EnvVar().WithName(config.EnvPrefix + "_DISPATCH_UPSTREAM_ADDR").WithValue(fmt.Sprintf("kubernetes:///%s.%s:dispatch", nn.Name, nn.Namespace)),
		applycorev1.EnvVar().WithName(config.EnvPrefix + "_DATASTORE_CONN_URI").WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(applycorev1.SecretKeySelector().WithName(r.cluster.Spec.SecretRef).WithKey("datastore_uri"))),
		applycorev1.EnvVar().WithName(config.EnvPrefix + "_GRPC_PRESHARED_KEY").WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(applycorev1.SecretKeySelector().WithName(r.cluster.Spec.SecretRef).WithKey("preshared_key"))),
	}

	probeCmd := []string{"grpc_health_probe", "-v", "-addr=localhost:50051"}

	volumes := make([]*applycorev1.VolumeApplyConfiguration, 0)
	volumeMounts := make([]*applycorev1.VolumeMountApplyConfiguration, 0)

	// TODO: validate that the secret exists before we start applying the deployment
	// TODO: allow overriding each service's certs
	if len(config.TLSSecretName) > 0 {
		depEnv = append(depEnv,
			applycorev1.EnvVar().WithName(config.EnvPrefix+"_GRPC_TLS_KEY_PATH").WithValue("/tls/tls.key"),
			applycorev1.EnvVar().WithName(config.EnvPrefix+"_GRPC_TLS_CERT_PATH").WithValue("/tls/tls.crt"),
			applycorev1.EnvVar().WithName(config.EnvPrefix+"_DISPATCH_CLUSTER_TLS_KEY_PATH").WithValue("/tls/tls.key"),
			applycorev1.EnvVar().WithName(config.EnvPrefix+"_DISPATCH_CLUSTER_TLS_CERT_PATH").WithValue("/tls/tls.crt"),
			// TODO: check that this works
			applycorev1.EnvVar().WithName(config.EnvPrefix+"_DISPATCH_UPSTREAM_CA_PATH").WithValue("/tls/tls.crt"),
			applycorev1.EnvVar().WithName(config.EnvPrefix+"_HTTP_TLS_KEY_PATH").WithValue("/tls/tls.key"),
			applycorev1.EnvVar().WithName(config.EnvPrefix+"_HTTP_TLS_CERT_PATH").WithValue("/tls/tls.crt"),
			applycorev1.EnvVar().WithName(config.EnvPrefix+"_DASHBOARD_TLS_KEY_PATH").WithValue("/tls/tls.key"),
			applycorev1.EnvVar().WithName(config.EnvPrefix+"_DASHBOARD_TLS_CERT_PATH").WithValue("/tls/tls.crt"),
			applycorev1.EnvVar().WithName(config.EnvPrefix+"_METRICS_TLS_KEY_PATH").WithValue("/tls/tls.key"),
			applycorev1.EnvVar().WithName(config.EnvPrefix+"_METRICS_TLS_CERT_PATH").WithValue("/tls/tls.crt"),
		)
		probeCmd = append(probeCmd, "-tls", "-tls-ca-cert=/tls/tls.crt")
		volumes = append(volumes, applycorev1.Volume().WithName("tls").WithSecret(applycorev1.SecretVolumeSource().WithDefaultMode(420).WithSecretName(config.TLSSecretName)))
		volumeMounts = append(volumeMounts, applycorev1.VolumeMount().WithName("tls").WithMountPath("/tls").WithReadOnly(true))
	}

	if len(migrateConfig.DatastoreTLSSecretName) > 0 {
		volumes = append(volumes, applycorev1.Volume().WithName("db-tls").WithSecret(applycorev1.SecretVolumeSource().WithDefaultMode(420).WithSecretName(migrateConfig.DatastoreTLSSecretName)))
		volumeMounts = append(volumeMounts, applycorev1.VolumeMount().WithName("db-tls").WithMountPath("/db-tls").WithReadOnly(true))
	}

	if len(migrateConfig.SpannerCredsSecretRef) > 0 {
		volumes = append(volumes, applycorev1.Volume().WithName("spanner").WithSecret(applycorev1.SecretVolumeSource().WithDefaultMode(420).WithSecretName(migrateConfig.SpannerCredsSecretRef).WithItems(
			applycorev1.KeyToPath().WithKey("credentials.json").WithPath("credentials.json"),
		)))
		volumeMounts = append(volumeMounts, applycorev1.VolumeMount().WithName("spanner").WithMountPath("/spanner-credentials").WithReadOnly(true))
	}

	dep = dep.WithSpec(applyappsv1.DeploymentSpec().
		WithReplicas(config.Replicas).
		WithSelector(applymetav1.LabelSelector().WithMatchLabels(map[string]string{"app.kubernetes.io/instance": name})).
		WithTemplate(applycorev1.PodTemplateSpec().WithLabels(map[string]string{"app.kubernetes.io/instance": name}).
			WithSpec(applycorev1.PodSpec().WithServiceAccountName(nn.Name).WithContainers(
				applycorev1.Container().WithName(name).WithImage(migrateConfig.TargetSpiceDBImage).
					WithCommand(config.SpiceDBCmd, "serve").
					WithEnv(depEnv...).
					WithPorts(
						applycorev1.ContainerPort().WithContainerPort(50051).WithName("grpc"),
						applycorev1.ContainerPort().WithContainerPort(50053).WithName("dispatch"),
						applycorev1.ContainerPort().WithContainerPort(8443).WithName("gateway"),
						applycorev1.ContainerPort().WithContainerPort(9090).WithName("metrics"),
					).WithLivenessProbe(
					applycorev1.Probe().WithExec(applycorev1.ExecAction().WithCommand(probeCmd...)).
						WithInitialDelaySeconds(60).WithFailureThreshold(5).WithPeriodSeconds(10).WithTimeoutSeconds(5),
				).WithReadinessProbe(
					applycorev1.Probe().WithExec(applycorev1.ExecAction().WithCommand(probeCmd...)).
						WithFailureThreshold(5).WithPeriodSeconds(10).WithTimeoutSeconds(5),
				).WithVolumeMounts(volumeMounts...),
			).WithVolumes(volumes...))))
	_, err = r.kclient.AppsV1().Deployments(nn.Namespace).Apply(ctx, dep, forceOwned)
	if err != nil {
		utilruntime.HandleError(err)
		return err
	}
	return nil
}

func (r *Reconciler) updateJob(ctx context.Context, nn types.NamespacedName, migrationHash string, config *MigrationConfig) error {
	name := fmt.Sprintf("%s-migrate-%s", nn.Name, migrationHash[:15])
	job := applybatchv1.Job(name, nn.Namespace)
	job = job.WithLabels(LabelsForComponent(nn.Name, ComponentMigrationJobLabelValue))
	job = job.WithAnnotations(map[string]string{
		SpiceDBMigrationRequirementsKey: migrationHash,
	})
	job = job.WithOwnerReferences(applymetav1.OwnerReference().
		WithName(nn.Name).
		WithKind(v1alpha1.AuthzedEnterpriseClusterKind).
		WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
		WithUID(r.cluster.UID))

	volumes := make([]*applycorev1.VolumeApplyConfiguration, 0)
	volumeMounts := make([]*applycorev1.VolumeMountApplyConfiguration, 0)

	// TODO: this is the same as the volumes/mounts for the deployment
	if len(config.DatastoreTLSSecretName) > 0 {
		volumes = append(volumes, applycorev1.Volume().WithName("db-tls").WithSecret(applycorev1.SecretVolumeSource().WithDefaultMode(420).WithSecretName(config.DatastoreTLSSecretName)))
		volumeMounts = append(volumeMounts, applycorev1.VolumeMount().WithName("db-tls").WithMountPath("/db-tls").WithReadOnly(true))
	}
	// TODO: this is the same as the volumes/mounts for the deployment
	if len(config.SpannerCredsSecretRef) > 0 {
		volumes = append(volumes, applycorev1.Volume().WithName("spanner").WithSecret(applycorev1.SecretVolumeSource().WithDefaultMode(420).WithSecretName(config.SpannerCredsSecretRef).WithItems(
			applycorev1.KeyToPath().WithKey("credentials.json").WithPath("credentials.json"),
		)))
		volumeMounts = append(volumeMounts, applycorev1.VolumeMount().WithName("spanner").WithMountPath("/spanner-credentials").WithReadOnly(true))
	}

	job = job.WithSpec(applybatchv1.JobSpec().WithTemplate(
		applycorev1.PodTemplateSpec().WithSpec(applycorev1.PodSpec().WithContainers(
			applycorev1.Container().WithName(name).WithImage(config.TargetSpiceDBImage).WithCommand(config.SpiceDBCmd, "migrate", "head").WithEnv(
				applycorev1.EnvVar().WithName(config.EnvPrefix+"_LOG_LEVEL").WithValue(config.LogLevel),
				applycorev1.EnvVar().WithName(config.EnvPrefix+"_DATASTORE_ENGINE").WithValue(config.DatastoreEngine),
				applycorev1.EnvVar().WithName(config.EnvPrefix+"_DATASTORE_CONN_URI").WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(applycorev1.SecretKeySelector().WithName(r.cluster.Spec.SecretRef).WithKey("datastore_uri"))),
				applycorev1.EnvVar().WithName(config.EnvPrefix+"_SECRETS").WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(applycorev1.SecretKeySelector().WithName(r.cluster.Spec.SecretRef).WithKey("migration_secrets"))),
			).WithVolumeMounts(volumeMounts...).WithPorts(
				applycorev1.ContainerPort().WithName("grpc").WithContainerPort(50051),
				applycorev1.ContainerPort().WithName("dispatch").WithContainerPort(50053),
				applycorev1.ContainerPort().WithName("gateway").WithContainerPort(8443),
				applycorev1.ContainerPort().WithName("prometheus").WithContainerPort(9090),
			).WithTerminationMessagePolicy(corev1.TerminationMessageFallbackToLogsOnError),
		).WithVolumes(volumes...).WithRestartPolicy(corev1.RestartPolicyNever))))

	_, err := r.kclient.BatchV1().Jobs(nn.Namespace).Apply(ctx, job, forceOwned)
	if err != nil {
		return err
	}
	return nil
}

// ensureService
// TODO: check if needs to be updated
func (r *Reconciler) ensureService(ctx context.Context, nn types.NamespacedName) func() {
	service, err := GetComponent[*corev1.Service](r.informers, corev1.SchemeGroupVersion.WithResource("services"), nn, ComponentServiceLabel)
	if err != nil {
		utilruntime.HandleError(err)
		return r.requeue(0)
	}

	if service != nil {
		return nil
	}

	newService := applycorev1.Service(nn.Name, nn.Namespace).WithLabels(LabelsForComponent(nn.Name, ComponentServiceLabel))
	newService = newService.WithOwnerReferences(applymetav1.OwnerReference().
		WithName(nn.Name).
		WithKind(v1alpha1.AuthzedEnterpriseClusterKind).
		WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
		WithUID(r.cluster.UID))
	newService = newService.WithSpec(applycorev1.ServiceSpec().WithSelector(LabelsForComponent(nn.Name, ComponentSpiceDBLabelValue)).WithPorts(
		applycorev1.ServicePort().WithName("grpc").WithPort(50051),
		applycorev1.ServicePort().WithName("dispatch").WithPort(50053),
		applycorev1.ServicePort().WithName("gateway").WithPort(8443),
		applycorev1.ServicePort().WithName("prometheus").WithPort(9090),
	))
	_, err = r.kclient.CoreV1().Services(nn.Namespace).Apply(ctx, newService, forceOwned)
	if err != nil {
		utilruntime.HandleError(err)
		return r.requeue(0)
	}

	return nil
}

// ensureRBAC
// TODO: check if needs to be updated
func (r *Reconciler) ensureRBAC(ctx context.Context) func() {
	serviceAccount, err := GetComponent[*corev1.ServiceAccount](r.informers, corev1.SchemeGroupVersion.WithResource("serviceaccounts"), r.cluster.NamespacedName(), ComponentServiceAccountLabel)
	if err != nil {
		utilruntime.HandleError(err)
		return r.requeue(0)
	}
	role, err := GetComponent[*rbacv1.Role](r.informers, rbacv1.SchemeGroupVersion.WithResource("roles"), r.cluster.NamespacedName(), ComponentRoleLabel)
	if err != nil {
		utilruntime.HandleError(err)
		return r.requeue(0)
	}
	roleBinding, err := GetComponent[*rbacv1.RoleBinding](r.informers, rbacv1.SchemeGroupVersion.WithResource("rolebindings"), r.cluster.NamespacedName(), ComponentRoleBindingLabel)
	if err != nil {
		utilruntime.HandleError(err)
		return r.requeue(0)
	}

	if serviceAccount != nil && role != nil && roleBinding != nil {
		return nil
	}

	newSA := applycorev1.ServiceAccount(r.cluster.Name, r.cluster.Namespace).WithLabels(LabelsForComponent(r.cluster.Name, ComponentServiceAccountLabel))
	newSA = newSA.WithOwnerReferences(applymetav1.OwnerReference().
		WithName(r.cluster.Name).
		WithKind(v1alpha1.AuthzedEnterpriseClusterKind).
		WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
		WithUID(r.cluster.UID))
	_, err = r.kclient.CoreV1().ServiceAccounts(r.cluster.Namespace).Apply(ctx, newSA, forceOwned)
	if err != nil {
		utilruntime.HandleError(err)
		return r.requeue(0)
	}

	newRole := applyrbacv1.Role(r.cluster.Name, r.cluster.Namespace).WithLabels(LabelsForComponent(r.cluster.Name, ComponentRoleLabel))
	newRole = newRole.WithRules(applyrbacv1.PolicyRule().WithAPIGroups("").WithResources("endpoints").WithVerbs("get", "list", "watch"))
	newRole = newRole.WithOwnerReferences(applymetav1.OwnerReference().
		WithName(r.cluster.Name).
		WithKind(v1alpha1.AuthzedEnterpriseClusterKind).
		WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
		WithUID(r.cluster.UID))
	_, err = r.kclient.RbacV1().Roles(r.cluster.Namespace).Apply(ctx, newRole, forceOwned)
	if err != nil {
		utilruntime.HandleError(err)
		return r.requeue(0)
	}

	newRoleBinding := applyrbacv1.RoleBinding(r.cluster.Name, r.cluster.Namespace).WithLabels(LabelsForComponent(r.cluster.Name, ComponentRoleBindingLabel))
	newRoleBinding = newRoleBinding.WithOwnerReferences(applymetav1.OwnerReference().
		WithName(r.cluster.Name).
		WithKind(v1alpha1.AuthzedEnterpriseClusterKind).
		WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
		WithUID(r.cluster.UID))
	newRoleBinding = newRoleBinding.WithRoleRef(applyrbacv1.RoleRef().WithKind("Role").WithName(r.cluster.Name))
	newRoleBinding = newRoleBinding.WithSubjects(applyrbacv1.Subject().WithNamespace(r.cluster.Namespace).WithKind("ServiceAccount").WithName(r.cluster.Name))
	_, err = r.kclient.RbacV1().RoleBindings(r.cluster.Namespace).Apply(ctx, newRoleBinding, forceOwned)
	if err != nil {
		utilruntime.HandleError(err)
		return r.requeue(0)
	}
	return nil
}

// TODO: take K as a parameter so that callers don't have to specify
func GetComponent[K metav1.Object](informers map[schema.GroupVersionResource]dynamicinformer.DynamicSharedInformerFactory, gvr schema.GroupVersionResource, owner types.NamespacedName, component string) ([]K, error) {
	var found []K

	ownedObjects, err := informers[gvr].ForResource(gvr).Informer().GetIndexer().ByIndex(OwningClusterIndex, owner.String())
	if err != nil {
		return nil, err
	}

	for _, d := range ownedObjects {
		unst, ok := d.(*unstructured.Unstructured)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("invalid object returned from index, expected unstructured, got: %T", d))
			continue
		}
		var obj *K
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unst.Object, &obj); err != nil {
			utilruntime.HandleError(fmt.Errorf("invalid object returned from index, expected %s, got: %s: %w", gvr.Resource, unst.GroupVersionKind(), err))
			continue
		}
		labels := (*obj).GetLabels()
		if labels == nil {
			continue
		}
		if labels[ComponentLabelKey] == component {
			found = append(found, *obj)
		}
	}
	return found, nil
}
