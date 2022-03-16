package cluster

import (
	"context"
	"crypto/sha256"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/davecgh/go-spew/spew"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

const (
	OwnerLabelKey                   = "authzed.com/cluster"
	ComponentLabelKey               = "authzed.com/cluster-component"
	ComponentSpiceDBLabelValue      = "spicedb"
	SpiceDBMigrationRequirementsKey = "authzed.com/spicedb-migration"
	SpiceDBConfigKey                = "authzed.com/spicedb-configuration"
	SpiceDBImage                    = "authzed-spicedb-enterprise:dev"
)

var (
	v1alpha1ClusterGVR = v1alpha1.SchemeGroupVersion.WithResource("clusters")
	authzedClusterGR   = v1alpha1ClusterGVR.GroupResource()
)

// MachineContext is passed between stages of the state machine and caches
// external state that later stages may need to reference.
type MachineContext struct {
	cluster           *v1alpha1.AuthzedEnterpriseCluster
	secret            *corev1.Secret
	validConfig       Config
	spiceDBDeployment *appsv1.Deployment
	secretHash        string
	headRevision      string
	migrationHash     string
	spiceDBConfigHash string
}

// syncCluster inspects the current AuthzedEnterpriseCluster object and ensures
// the desired state is persisted on the cluster.
// If syncCluster returns an error, the key for the cluster is re-queued (with a
// rate limit).
// `cluster` is a copy of the object from the cache and is safe to mutate.
func (c *Controller) syncCluster(ctx context.Context, cluster *v1alpha1.AuthzedEnterpriseCluster) error {
	klog.V(4).Infof("syncing cluster %s", cluster.ObjectMeta)

	// unmanaged stacks are not watched, but may be in the queue due to a sync
	// of a dependent object
	if IsUnmanaged(cluster) {
		return nil
	}

	m := &MachineContext{
		cluster: cluster,
	}

	// TODO: generate default secret if missing
	// get the secret and see if it's changed
	if err := c.getOrAdoptSecret(ctx, m); err != nil {
		return err
	}

	// Update status with observed generation / observed secret hash
	if cluster.ObjectMeta.Generation != cluster.Status.ObservedGeneration || m.secretHash != cluster.Status.SecretHash {
		fragment := v1alpha1.AuthzedEnterpriseCluster{
			Status: v1alpha1.ClusterStatus{
				ObservedGeneration: cluster.ObjectMeta.Generation,
				SecretHash:         m.secretHash,
				Conditions: []metav1.Condition{{
					Type:               "Validating", // TODO: constants, etc
					Status:             metav1.ConditionTrue,
					Reason:             "ConfigChanged",
					LastTransitionTime: metav1.NewTime(time.Now()),
					Message:            fmt.Sprintf("Validating new generation %d with secret hash %s", cluster.ObjectMeta.Generation, m.secretHash),
				}},
			},
		}
		if err := c.client.Status().Patch(ctx, &fragment, client.Apply); err != nil {
			return err
		}
	}

	if err := c.validConfig(m); err != nil {
		fragment := v1alpha1.AuthzedEnterpriseCluster{
			Status: v1alpha1.ClusterStatus{
				Conditions: []metav1.Condition{{
					Type:               "InvalidConfig", // TODO: constants, etc
					Status:             metav1.ConditionTrue,
					Reason:             "ValidatingFailed",
					LastTransitionTime: metav1.NewTime(time.Now()),
					Message:            fmt.Sprintf("Errors found during config validation: %v", err),
				}},
			},
		}
		if err := c.client.Status().Patch(ctx, &fragment, client.Apply); err != nil {
			return err
		}
		// there's nothing to do until the config is fixed.
		return nil
	}

	// config is valid

	// we only need to check migrations / migrate if:
	// 	- there has been a relevant config change (the datastore uri or type has changed).
	// 	     - we can detect this because we annotate the deployment with a hash of that info
	//  - the specified version differs from the deployed version (new version may require a migration)

	// TODO: get head revision
	// m.headRevision = getHeadRevision()

	if err := c.getSpicedbDeployment(m); err != nil {
		return err
	}

	// determine if config / state has changed in some way that would require us to check
	// whether the datastore is fully migrated
	checkMigrations, err := c.migrationCheckRequired(m)
	if err != nil {
		return err
	}

	if checkMigrations {
		fragment := v1alpha1.AuthzedEnterpriseCluster{
			Status: v1alpha1.ClusterStatus{
				Conditions: []metav1.Condition{{
					Type:               "Migrating", // TODO: constants, etc
					Status:             metav1.ConditionTrue,
					Reason:             "VerifyingMigrationLevel",
					LastTransitionTime: metav1.NewTime(time.Now()),
					Message:            "Checking if cluster's datastore is migrated",
				}},
			},
		}
		if err := c.client.Status().Patch(ctx, &fragment, client.Apply); err != nil {
			return err
		}

		migrationsRequired, err := c.checkDatastoreMigrationLevel(m)
		if err != nil {
			return err
		}
		if migrationsRequired {
			fragment := v1alpha1.AuthzedEnterpriseCluster{
				Status: v1alpha1.ClusterStatus{
					Conditions: []metav1.Condition{{
						Type:               "Migrating", // TODO: constants, etc
						Status:             metav1.ConditionTrue,
						Reason:             "MigratingDatastoreToHead",
						LastTransitionTime: metav1.NewTime(time.Now()),
						Message:            fmt.Sprintf("Migrating %s datastore to %s", m.validConfig.DatastoreEngine, m.headRevision),
					}},
				},
			}
			if err := c.client.Status().Patch(ctx, &fragment, client.Apply); err != nil {
				return err
			}

			// actually migrate
			if err := c.migrateHead(m); err != nil {
				fragment := v1alpha1.AuthzedEnterpriseCluster{
					Status: v1alpha1.ClusterStatus{
						Conditions: []metav1.Condition{{
							Type:               "Migrating", // TODO: constants, etc
							Status:             metav1.ConditionFalse,
							Reason:             "MigrateError",
							LastTransitionTime: metav1.NewTime(time.Now()),
							Message:            fmt.Sprintf("Failed while migrating: %v.", err),
						}},
					},
				}
				if err := c.client.Status().Patch(ctx, &fragment, client.Apply); err != nil {
					return err
				}
			}
		}
	}

	// datastore is up and fully migrated

	// ensure deployment exists with proper config
	deploymentUpdateRequired, err := c.deploymentUpdateRequired(m)
	if err != nil {
		return err
	}

	if deploymentUpdateRequired {
		if err := c.updateDeployment(ctx, m); err != nil {
			return err
		}
	}

	return nil
}

// getOrAdoptSecret will return the secret from the cache, or label the secret
// so that it will be in the cache in the future (if labelling succeeds, the
// newly labelled secret will be returned). Returns nil if cluster has no
// secretRef.
func (c *Controller) getOrAdoptSecret(ctx context.Context, m *MachineContext) (err error) {
	if m.cluster.Spec.SecretRef == "" {
		return
	}
	secretsGVR := corev1.SchemeGroupVersion.WithResource("secrets")
	secrets, err := c.informers[secretsGVR].ForResource(secretsGVR).Informer().GetIndexer().ByIndex(OwningClusterIndex, m.cluster.Namespace+"/"+m.cluster.Name)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	switch len(secrets) {
	case 0:
		// secret is not in cache, which means it's not labelled for the cluster
		// fetch it and add the label to it.
		m.secret, err = c.kclient.CoreV1().Secrets(m.cluster.Namespace).Apply(ctx, applycorev1.Secret(m.cluster.Spec.SecretRef, m.cluster.Namespace).WithLabels(map[string]string{
			OwnerLabelKey: m.cluster.Name,
		}), metav1.ApplyOptions{FieldManager: "authzed-operator", Force: true})
		if err != nil {
			return
		}
		c.recorder.Event(m.secret, "Adopted", "ReferencedByCluster", "Secret was referenced as the secret source for an AuthzedEnterpriseCluster; it has been labelled to mark it as part of the configuration for that cluster.")
	case 1:
		var ok bool
		m.secret, ok = secrets[0].(*corev1.Secret)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("non-secret object found in secret informer cache for %s/%s; should not be possible", m.cluster.Namespace, m.cluster.Spec.SecretRef))
		}
	default:
		utilruntime.HandleError(fmt.Errorf("more than one secret found for %s/%s; should not be possible", m.cluster.Namespace, m.cluster.Spec.SecretRef))
	}
	m.secretHash, err = HashSecret(m.secret)
	return err
}

type Config struct {
	DatastoreEngine string
	DatastoreURI    string
	SpannerCreds    string
}

// validConfig checks that the values in the config + the secret are sane
// TODO: can we just re-use cue here?
func (c *Controller) validConfig(m *MachineContext) error {
	datastoreEngine, ok := m.cluster.Spec.Config["datastore_engine"]
	if !ok {
		return fmt.Errorf("%s Config.datastore_engine is a required field", m.cluster.Name)
	}

	datastoreURI, ok := m.secret.Data["datastore_uri"]
	if !ok {
		return fmt.Errorf("secret %s must contain a datastore-uri field", m.secret.Name)
	}

	m.validConfig = Config{
		DatastoreEngine: datastoreEngine,
		DatastoreURI:    string(datastoreURI),
		SpannerCreds:    m.cluster.Spec.Config["spanner_credentials"],
	}
	return nil
}

func (c *Controller) getSpicedbDeployment(m *MachineContext) error {
	ownedDeployments, err := c.getOwnedObjects(m.cluster, appsv1.SchemeGroupVersion.WithResource("deployments"))
	if err != nil {
		return err
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
	m.spiceDBDeployment = spiceDeployment
	return nil
}

// migrationCheckRequired returns true if the current state requires that we
// check the migration level of the database. This can be required if there
// has been a relevant config change (database-uri, for example) or if the
// cluster is initializing.
func (c *Controller) migrationCheckRequired(m *MachineContext) (bool, error) {
	if m.spiceDBDeployment == nil {
		return true, nil
	}
	// check the migration requirements hash
	var err error
	m.migrationHash, err = HashMigrationRequirements(&m.validConfig, m.headRevision)
	if err != nil {
		return false, err
	}

	// no annotations means we need to check
	annotations := m.spiceDBDeployment.Annotations
	if annotations == nil {
		return true, nil
	}

	// if hash is different, migration check is required
	return annotations[SpiceDBMigrationRequirementsKey] == m.migrationHash, nil
}

func (c *Controller) checkDatastoreMigrationLevel(m *MachineContext) (bool, error) {
	// TODO
	return false, nil
}

func (c *Controller) migrateHead(m *MachineContext) error {
	// TODO
	return nil
}

// deploymentUpdateRequired returns true if the deployment on the cluster needs
// to be updated to match the current config
func (c *Controller) deploymentUpdateRequired(m *MachineContext) (bool, error) {
	// if there's no spice deployment, need to create one
	if m.spiceDBDeployment == nil {
		return true, nil
	}

	// calculate the config hash
	var err error
	m.spiceDBConfigHash, err = HashDeploymentConfig(&m.validConfig)
	if err != nil {
		return false, err
	}

	// no annotations means we need to update
	annotations := m.spiceDBDeployment.Annotations
	if annotations == nil {
		return true, nil
	}

	// if hash is different, update is required
	return annotations[SpiceDBConfigKey] == m.spiceDBConfigHash, nil
}

func (c *Controller) updateDeployment(ctx context.Context, m *MachineContext) error {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-spicedb", m.cluster.Name),
			Namespace: m.cluster.Namespace,
			Labels: map[string]string{
				OwnerLabelKey:     fmt.Sprintf("%s/%s", m.cluster.Namespace, m.cluster.Name),
				ComponentLabelKey: ComponentSpiceDBLabelValue,
			},
			Annotations: map[string]string{
				SpiceDBMigrationRequirementsKey: m.migrationHash,
				SpiceDBConfigKey:                m.spiceDBConfigHash,
			},
		},
		Spec: appsv1.DeploymentSpec{
			// TODO: label selector
			Template: corev1.PodTemplateSpec{
				// TODO: labels
				ObjectMeta: metav1.ObjectMeta{},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Image: SpiceDBImage,
						},
					},
				},
			},
		},
	}

	return c.client.Patch(ctx, deployment, client.Apply)
}

func HashSecret(s *corev1.Secret) (string, error) {
	// using sha256 since the secret data is sensitive
	hasher := sha256.New()
	printer := spew.ConfigState{
		Indent:         " ",
		SortKeys:       true,
		DisableMethods: true,
		SpewKeys:       true,
	}
	_, err := printer.Fprintf(hasher, "%#v", s.Data)
	if err != nil {
		return "", err
	}
	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum(nil))), nil
}

func HashMigrationRequirements(c *Config, headRevision string) (string, error) {
	// using sha256 since the database uri may contain a secret
	hasher := sha256.New()
	if _, err := fmt.Fprint(hasher, c.DatastoreEngine); err != nil {
		return "", err
	}
	if _, err := fmt.Fprint(hasher, c.DatastoreURI); err != nil {
		return "", err
	}
	if _, err := fmt.Fprint(hasher, headRevision); err != nil {
		return "", err
	}
	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum(nil))), nil
}

func HashDeploymentConfig(c *Config) (string, error) {
	// using fnv32 - no sensitive data is hashed
	hasher := fnv.New32()
	if _, err := fmt.Fprint(hasher, c.DatastoreEngine); err != nil {
		return "", err
	}
	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum(nil))), nil
}
