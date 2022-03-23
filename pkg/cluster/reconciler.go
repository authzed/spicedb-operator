package cluster

import (
	"context"
	"crypto/subtle"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	applybatchv1 "k8s.io/client-go/applyconfigurations/batch/v1"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/klog/v2"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/util"
)

const (
	OwnerLabelKey                   = "authzed.com/cluster"
	ComponentLabelKey               = "authzed.com/cluster-component"
	ComponentSpiceDBLabelValue      = "spicedb"
	ComponentMigrationJobLabelValue = "migration-job"
	SpiceDBMigrationRequirementsKey = "authzed.com/spicedb-migration"
	SpiceDBConfigKey                = "authzed.com/spicedb-configuration"
	SpiceDBTag                      = "dev"
)

var (
	v1alpha1ClusterGVR = v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.AuthzedEnterpriseClusterResourceName)
	authzedClusterGR   = v1alpha1ClusterGVR.GroupResource()
)

// DoNotRequeue can be returned from syncCluster to signal that processing is
// complete but shouldn't be requeued.
var DoNotRequeue error = nil

// syncCluster inspects the current AuthzedEnterpriseCluster object and ensures
// the desired state is persisted on the cluster.
// If syncCluster returns an error, the key for the cluster is re-queued (with a
// rate limit).
// `cluster` is a copy of the object from the cache and is safe to mutate.
func (c *Controller) syncCluster(ctx context.Context, cluster *v1alpha1.AuthzedEnterpriseCluster) error {
	klog.V(4).Infof("syncing cluster %s/%s", cluster.Namespace, cluster.Name)

	// paused objects are not watched, but may be in the queue due to a sync
	// of a dependent object
	if util.IsPaused(cluster) {
		if meta.FindStatusCondition(cluster.Status.Conditions, v1alpha1.ConditionTypePaused) != nil {
			return DoNotRequeue
		}
		patch := v1alpha1.NewPatch(cluster.NamespacedName(), cluster.Generation)
		meta.SetStatusCondition(&patch.Status.Conditions, v1alpha1.NewPausedCondition())
		if err := c.PatchStatus(ctx, patch); err != nil {
			return err
		}
		return DoNotRequeue
	}

	// TODO: generate default secret if missing
	// get the secret and see if it's changed
	secret, secretHash, err := c.getOrAdoptSecret(ctx, cluster.NamespacedName(), cluster.Spec.SecretRef)
	if err != nil {
		return err
	}

	if cluster.ObjectMeta.Generation != cluster.Status.ObservedGeneration || secretHash != cluster.Status.SecretHash {
		patch := v1alpha1.NewPatch(cluster.NamespacedName(), cluster.Generation)
		patch.Status.ObservedGeneration = cluster.ObjectMeta.Generation
		patch.Status.SecretHash = secretHash
		meta.SetStatusCondition(&patch.Status.Conditions, v1alpha1.NewValidatingConfigCondition(secretHash))
		if err := c.PatchStatus(ctx, patch); err != nil {
			return err
		}
	}

	config, err := c.validConfig(cluster.Spec.Config, secret)
	if err != nil {
		patch := v1alpha1.NewPatch(cluster.NamespacedName(), cluster.Generation)
		meta.SetStatusCondition(&patch.Status.Conditions, v1alpha1.NewInvalidConfigCondition(secretHash, err))
		if err := c.PatchStatus(ctx, patch); err != nil {
			return err
		}
		// if the config is invalid, there's no work to do until it has changed
		return DoNotRequeue
	}

	// config is valid

	spiceDBDeployment, err := c.getSpicedbDeployment(cluster.NamespacedName())
	if err != nil {
		return err
	}

	// check migration level if
	// 	- there's no deployment
	//  - there's been a relevant config change (datastore uri, engine, credentials, target spicedb version)

	// determine if config / state has changed in some way that would require us to check
	// whether the datastore is fully migrated
	checkMigrations, err := c.migrationCheckRequired(spiceDBDeployment, &config.MigrationConfig)
	if err != nil {
		return err
	}

	if checkMigrations {
		patch := v1alpha1.NewPatch(cluster.NamespacedName(), cluster.Generation)
		meta.SetStatusCondition(&patch.Status.Conditions, v1alpha1.NewVerifyingMigrationCondition())
		if err := c.PatchStatus(ctx, patch); err != nil {
			return err
		}

		migrationJob, err := c.getMigrationJob(cluster.NamespacedName())
		if err != nil {
			return err
		}

		migrationsRequired, err := c.checkDatastoreMigrationLevel(spiceDBDeployment)
		if err != nil {
			return err
		}
		if migrationsRequired {
			patch := v1alpha1.NewPatch(cluster.NamespacedName(), cluster.Generation)
			meta.SetStatusCondition(&patch.Status.Conditions, v1alpha1.NewMigratingCondition(config.DatastoreEngine, "head"))
			if err := c.PatchStatus(ctx, patch); err != nil {
				return err
			}

			// actually migrate
			if err := c.migrateHead(ctx, cluster, migrationJob, &config.MigrationConfig); err != nil {
				// if migration fails for a non-requable reason, pause
				patch := v1alpha1.NewPatch(cluster.NamespacedName(), cluster.Generation)
				meta.SetStatusCondition(&patch.Status.Conditions, v1alpha1.NewMigrationFailedCondition(config.DatastoreEngine, "head", err))
				if err := c.PatchStatus(ctx, patch); err != nil {
					return err
				}
				if err := c.selfPause(ctx, cluster.NamespacedName(), cluster.Generation, cluster.UID); err != nil {
					return err
				}
				return DoNotRequeue
			}
		}
	}

	// datastore is up and fully migrated

	// ensure deployment exists with proper config
	deploymentUpdateRequired, err := c.deploymentUpdateRequired(spiceDBDeployment, &config.SpiceConfig)
	if err != nil {
		return err
	}

	if deploymentUpdateRequired {
		if err := c.updateDeployment(ctx, cluster.NamespacedName(), cluster.Annotations[SpiceDBMigrationRequirementsKey], cluster.Annotations[SpiceDBConfigKey]); err != nil {
			return err
		}
	}

	return nil
}

func (c *Controller) selfPause(ctx context.Context, nn types.NamespacedName, generation int64, uid types.UID) error {
	patch := v1alpha1.NewPatch(nn, generation)
	meta.SetStatusCondition(&patch.Status.Conditions, v1alpha1.NewSelfPausedCondition())
	if err := c.PatchStatus(ctx, patch); err != nil {
		return err
	}
	patch = v1alpha1.NewPatch(nn, generation)
	patch.ObjectMeta.Labels = map[string]string{
		util.PausedControllerSelectorKey: string(uid),
	}
	if err := c.Patch(ctx, patch); err != nil {
		return err
	}
	return DoNotRequeue
}

// getOrAdoptSecret will return the secret from the cache, or label the secret
// so that it will be in the cache in the future (if labelling succeeds, the
// newly labelled secret will be returned). Returns nil if cluster has no
// secretRef.
func (c *Controller) getOrAdoptSecret(ctx context.Context, nn types.NamespacedName, secretRef string) (secret *corev1.Secret, secretHash string, err error) {
	if secretRef == "" {
		return
	}
	secretsGVR := corev1.SchemeGroupVersion.WithResource("secrets")
	secrets, err := c.informers[secretsGVR].ForResource(secretsGVR).Informer().GetIndexer().ByIndex(OwningClusterIndex, nn.String())
	if err != nil {
		return
	}
	switch len(secrets) {
	case 0:
		// secret is not in cache, which means it's not labelled for the cluster
		// fetch it and add the label to it.
		secret, err = c.kclient.CoreV1().Secrets(nn.Namespace).Apply(ctx, applycorev1.Secret(secretRef, nn.Namespace).WithLabels(map[string]string{
			OwnerLabelKey: nn.Name,
		}), metav1.ApplyOptions{FieldManager: "authzed-operator", Force: true})
		if err != nil {
			err = RequeueableError(err)
		}
		c.recorder.Event(secret, "Adopted", "ReferencedByCluster", "Secret was referenced as the secret source for an AuthzedEnterpriseCluster; it has been labelled to mark it as part of the configuration for that cluster.")
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
		return
	}
	secretHash, err = util.SecureHashObject(secret)
	return secret, secretHash, RequeueableError(err)
}

// validConfig checks that the values in the config + the secret are sane
// TODO: can we just re-use cue here?
func (c *Controller) validConfig(config map[string]string, secret *corev1.Secret) (*Config, error) {
	datastoreEngine, ok := config["datastore_engine"]
	if !ok {
		return nil, fmt.Errorf("Config.datastore_engine is a required field")
	}

	datastoreURI, ok := secret.Data["datastore_uri"]
	if !ok {
		return nil, fmt.Errorf("secret must contain a datastore-uri field")
	}

	return &Config{MigrationConfig: MigrationConfig{
		DatastoreEngine:  datastoreEngine,
		DatastoreURI:     string(datastoreURI),
		SpannerCreds:     config["spanner_credentials"],
		TargetSpiceDBTag: SpiceDBTag,
	}}, nil
}

func (c *Controller) getSpicedbDeployment(nn types.NamespacedName) (*appsv1.Deployment, error) {
	ownedDeployments, err := c.getOwnedObjects(nn, appsv1.SchemeGroupVersion.WithResource("deployments"))
	if err != nil {
		return nil, err
	}

	var spiceDeployment *appsv1.Deployment
	for _, d := range ownedDeployments {
		deployment, ok := d.(*appsv1.Deployment)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("invalid object returned from index, expected deployment, got: %T", d))
		}
		labels := deployment.GetLabels()
		if labels == nil {
			continue
		}
		if labels[ComponentLabelKey] == ComponentSpiceDBLabelValue {
			spiceDeployment = deployment
			break
		}
	}
	return spiceDeployment, nil
}

func (c *Controller) getMigrationJob(nn types.NamespacedName) (*batchv1.Job, error) {
	ownedJobs, err := c.getOwnedObjects(nn, batchv1.SchemeGroupVersion.WithResource("jobs"))
	if err != nil {
		return nil, err
	}

	var migrationJob *batchv1.Job
	for _, d := range ownedJobs {
		job, ok := d.(*batchv1.Job)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("invalid object returned from index, expected deployment, got: %T", d))
			continue
		}
		labels := job.GetLabels()
		if labels == nil {
			continue
		}
		if labels[ComponentLabelKey] == ComponentMigrationJobLabelValue {
			migrationJob = job
			break
		}
	}
	return migrationJob, nil
}

// migrationCheckRequired returns true if the current state requires that we
// check the migration level of the database. This can be required if there
// has been a relevant config change (database-uri, for example) or if the
// cluster is initializing.
func (c *Controller) migrationCheckRequired(spiceDBDeployment *appsv1.Deployment, config *MigrationConfig) (bool, error) {
	if spiceDBDeployment == nil {
		return true, nil
	}

	// check the migration requirements hash
	migrationHash, err := util.SecureHashObject(config)
	if err != nil {
		return false, err
	}

	// no annotations means we need to check
	annotations := spiceDBDeployment.Annotations
	if annotations == nil {
		return true, nil
	}

	// if hash is different, migration check is required
	return subtle.ConstantTimeCompare([]byte(annotations[SpiceDBMigrationRequirementsKey]), []byte(migrationHash)) == 1, nil
}

func (c *Controller) checkDatastoreMigrationLevel(spiceDBDeployment *appsv1.Deployment) (bool, error) {
	// if there is no deployment, we need to migrate
	if spiceDBDeployment == nil {
		return true, nil
	}
	// TODO: exec to run `head` command, determine if it's the level we want

	// executor := exec.DefaultRemoteExecutor{}
	// req := restClient.Post().
	// 	Resource("pods").
	// 	Name(pod.Name).
	// 	Namespace(pod.Namespace).
	// 	SubResource("exec")
	// req.VersionedParams(&corev1.PodExecOptions{
	// 	Container: containerName,
	// 	Command:   p.Command,
	// 	Stdin:     p.Stdin,
	// 	Stdout:    p.Out != nil,
	// 	Stderr:    p.ErrOut != nil,
	// 	TTY:       t.Raw,
	// }, scheme.ParameterCodec)
	// executor.Execute("POST", req.URL(), p.Config, p.In, p.Out, p.ErrOut, t.Raw, sizeQueue)

	return false, nil
}

func (c *Controller) migrateHead(ctx context.Context, cluster *v1alpha1.AuthzedEnterpriseCluster, migrationJob *batchv1.Job, migrationConfig *MigrationConfig) error {
	migrationJobRequired, err := c.migrationJobApplyRequired(migrationJob, migrationConfig)
	if err != nil {
		return RequeueableError(err)
	}
	if migrationJobRequired {
		// TODO: this is calculated in the step above too
		migrationConfigHash, err := util.SecureHashObject(migrationConfig)
		if err != nil {
			return err

		}
		if err := c.updateJob(ctx, cluster.NamespacedName(), migrationConfigHash); err != nil {
			return RequeueableError(err)
		}
	}

	findJobCondition := func(conditions []batchv1.JobCondition, conditionType batchv1.JobConditionType) *batchv1.JobCondition {
		for i := range conditions {
			if conditions[i].Type == conditionType {
				return &conditions[i]
			}
		}
		return nil
	}

	// if failed, report status
	if c := findJobCondition(migrationJob.Status.Conditions, batchv1.JobFailed); c != nil {
		// TODO: pause?
		return fmt.Errorf("migration job failed: %s", c.Message)
	}

	// if done, delete job
	if findJobCondition(migrationJob.Status.Conditions, batchv1.JobComplete) != nil {
		// done, delete job
		if err := c.kclient.BatchV1().Jobs(migrationJob.Namespace).Delete(ctx, migrationJob.Name, metav1.DeleteOptions{}); err != nil {
			return RequeueableError(err)
		}
		return nil
	}

	// otherwise, it's created and still running, just wait
	c.queue.AddAfter(cluster, 5*time.Second)

	return nil
}

// deploymentUpdateRequired returns true if the deployment on the cluster needs
// to be updated to match the current config
func (c *Controller) migrationJobApplyRequired(migrationJob *batchv1.Job, migrationConfig *MigrationConfig) (bool, error) {
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
	return annotations[SpiceDBMigrationRequirementsKey] == migrationConfigHash, nil
}

// deploymentUpdateRequired returns true if the deployment on the cluster needs
// to be updated to match the current config
func (c *Controller) deploymentUpdateRequired(spiceDBDeployment *appsv1.Deployment, config *SpiceConfig) (bool, error) {
	// if there's no spice deployment, need to create one
	if spiceDBDeployment == nil {
		return true, nil
	}

	// calculate the config hash
	spiceDBConfigHash, err := util.SecureHashObject(config)
	if err != nil {
		return false, err
	}

	// no annotations means we need to update
	annotations := spiceDBDeployment.Annotations
	if annotations == nil {
		return true, nil
	}

	// if hash is different, update is required
	return annotations[SpiceDBConfigKey] == spiceDBConfigHash, nil
}

func (c *Controller) updateDeployment(ctx context.Context, nn types.NamespacedName, migrationHash, spiceDBConfigHash string) error {
	dep := applyappsv1.Deployment(fmt.Sprintf("%s-spicedb", nn.Name), nn.Namespace)
	dep = dep.WithLabels(map[string]string{
		OwnerLabelKey:     nn.String(),
		ComponentLabelKey: ComponentSpiceDBLabelValue,
	})
	dep = dep.WithAnnotations(map[string]string{
		SpiceDBMigrationRequirementsKey: migrationHash,
		SpiceDBConfigKey:                spiceDBConfigHash,
	})
	dep = dep.WithSpec(applyappsv1.DeploymentSpec().WithTemplate(applycorev1.PodTemplateSpec().WithSpec(applycorev1.PodSpec().WithContainers(
		applycorev1.Container().WithImage(SpiceDBTag),
	))))
	_, err := c.kclient.AppsV1().Deployments(nn.Namespace).Apply(ctx, dep, metav1.ApplyOptions{FieldManager: "authzed-operator", Force: true})
	if err != nil {
		return RequeueableError(err)
	}
	return nil
}

func (c *Controller) updateJob(ctx context.Context, nn types.NamespacedName, migrationHash string) error {
	job := applybatchv1.Job(fmt.Sprintf("%s-migrate-%s", nn.Name, migrationHash), nn.Namespace)
	job = job.WithLabels(map[string]string{
		OwnerLabelKey:     nn.String(),
		ComponentLabelKey: ComponentMigrationJobLabelValue,
	})
	job = job.WithAnnotations(map[string]string{
		SpiceDBMigrationRequirementsKey: migrationHash,
	})
	job = job.WithSpec(applybatchv1.JobSpec().WithTemplate(applycorev1.PodTemplateSpec().WithSpec(applycorev1.PodSpec().WithContainers(
		applycorev1.Container().WithImage(SpiceDBTag).WithCommand("migrate", "head"),
	))))

	_, err := c.kclient.BatchV1().Jobs(nn.Namespace).Apply(ctx, job, metav1.ApplyOptions{FieldManager: "authzed-operator", Force: true})
	if err != nil {
		return RequeueableError(err)
	}
	return nil
}
