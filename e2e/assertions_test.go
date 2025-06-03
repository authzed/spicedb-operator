//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ktypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

func AssertMigrationJobCleanupFunc(ctx context.Context, namespace string, kclient kubernetes.Interface) func(owner string) {
	return func(owner string) {
		ctx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)

		Eventually(func(g Gomega) {
			jobs, err := kclient.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", metadata.ComponentLabelKey, metadata.ComponentMigrationJobLabelValue, metadata.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(jobs.Items)).To(BeZero())
		}).Should(Succeed())
	}
}

func AssertServiceAccountFunc(ctx context.Context, namespace string, kclient kubernetes.Interface) func(name string, annotations map[string]string, owner string) {
	return func(name string, annotations map[string]string, owner string) {
		ctx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)

		var serviceAccounts *corev1.ServiceAccountList
		Eventually(func(g Gomega) {
			var err error
			serviceAccounts, err = kclient.CoreV1().ServiceAccounts(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", metadata.ComponentLabelKey, metadata.ComponentServiceAccountLabel, metadata.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(serviceAccounts.Items)).To(Equal(1))
			g.Expect(serviceAccounts.Items[0].GetName()).To(Equal(name))
			for k, v := range annotations {
				g.Expect(serviceAccounts.Items[0].GetAnnotations()).To(HaveKeyWithValue(k, v))
			}
		}).Should(Succeed())
	}
}

func AssertPDBFunc(ctx context.Context, namespace string, kclient kubernetes.Interface) func(name string, owner string) {
	return func(name string, owner string) {
		ctx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)

		var pdbs *policyv1.PodDisruptionBudgetList
		Eventually(func(g Gomega) {
			var err error
			pdbs, err = kclient.PolicyV1().PodDisruptionBudgets(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", metadata.ComponentLabelKey, metadata.ComponentPDBLabel, metadata.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(pdbs.Items)).To(Equal(1))
			g.Expect(pdbs.Items[0].GetName()).To(Equal(name))
		}).Should(Succeed())
	}
}

func AssertHealthySpiceDBClusterFunc(ctx context.Context, namespace string, kclient kubernetes.Interface) func(image, owner string, logMatcher types.GomegaMatcher) {
	return func(image, owner string, logMatcher types.GomegaMatcher) {
		ctx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)

		var deps *appsv1.DeploymentList
		Eventually(func(g Gomega) {
			var err error
			deps, err = kclient.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", metadata.ComponentLabelKey, metadata.ComponentSpiceDBLabelValue, metadata.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(deps.Items)).To(Equal(1))
			if len(image) > 0 {
				g.Expect(deps.Items[0].Spec.Template.Spec.Containers[0].Image).To(Equal(image))
			}
			g.Expect(deps.Items[0].Status.AvailableReplicas).ToNot(BeZero())

			endpoint, err := kclient.CoreV1().Endpoints(namespace).Get(ctx, owner, metav1.GetOptions{})
			g.Expect(err).To(Succeed())
			g.Expect(endpoint).ToNot(BeNil())
		}).Should(Succeed())

		By("not having startup warnings")
		var buf bytes.Buffer
		Tail(&deps.Items[0], func(g Gomega) {
			g.Eventually(buf.Len()).ShouldNot(BeZero())
			g.Consistently(buf.String()).Should(logMatcher)
		}, GinkgoWriter, &buf)
	}
}

func AssertDependentResourceCleanupFunc(ctx context.Context, namespace string, kclient kubernetes.Interface) func(owner, secretName string) {
	return func(owner, secretName string) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		By("waiting for resources to be GCd in " + namespace)

		// the secret should remain despite deleting the cluster
		Consistently(func(g Gomega) {
			secret, err := kclient.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
			g.Expect(err).To(Succeed())
			g.Expect(secret).ToNot(BeNil())
		}).Should(Succeed())

		// dependent resources should all be cleaned up
		Eventually(func(g Gomega) {
			list, err := kclient.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", metadata.ComponentLabelKey, metadata.ComponentSpiceDBLabelValue, metadata.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(list.Items)).To(BeZero())
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			list, err := kclient.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", metadata.ComponentLabelKey, metadata.ComponentServiceLabel, metadata.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(list.Items)).To(BeZero())
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			list, err := kclient.CoreV1().ServiceAccounts(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", metadata.ComponentLabelKey, metadata.ComponentServiceAccountLabel, metadata.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(list.Items)).To(BeZero())
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			list, err := kclient.RbacV1().Roles(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", metadata.ComponentLabelKey, metadata.ComponentRoleLabel, metadata.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(list.Items)).To(BeZero())
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			list, err := kclient.RbacV1().RoleBindings(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s,%s=%s", metadata.ComponentLabelKey, metadata.ComponentRoleBindingLabel, metadata.OwnerLabelKey, owner),
			})
			g.Expect(err).To(Succeed())
			g.Expect(len(list.Items)).To(BeZero())
		}).Should(Succeed())
	}
}

func AssertMigrationsCompletedFunc(ctx context.Context, namespace string, kclient kubernetes.Interface, client dynamic.Interface) func(image, migration, phase, name, datastoreEngine string) {
	return func(image, migration, phase, name, datastoreEngine string) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		var condition *metav1.Condition
		Eventually(func(g Gomega) {
			watchCtx, watchCancel := context.WithTimeout(ctx, 3*time.Minute)
			defer watchCancel()
			Watch(watchCtx, client, v1alpha1ClusterGVR, ktypes.NamespacedName{Name: name, Namespace: namespace}, "0", func(c *v1alpha1.SpiceDBCluster) bool {
				condition = c.FindStatusCondition("Migrating")
				logr.FromContextOrDiscard(ctx).Info("watch event", "status", c.Status)
				return condition == nil
			})
			// validate the specific migration if specified
			if migration == "" {
				g.Expect(condition).ToNot(BeNil())
			} else {
				g.Expect(condition).To(EqualCondition(v1alpha1.NewMigratingCondition(datastoreEngine, migration)))
			}
		}).Should(Succeed())

		var job *batchv1.Job
		watchCtx, watchCancel := context.WithTimeout(ctx, 3*time.Minute)
		defer watchCancel()
		watcher, err := kclient.BatchV1().Jobs(namespace).Watch(watchCtx, metav1.ListOptions{
			Watch:           true,
			ResourceVersion: "0",
			LabelSelector:   fmt.Sprintf("%s=%s", metadata.ComponentLabelKey, metadata.ComponentMigrationJobLabelValue),
		})
		Expect(err).To(Succeed())

		matchingJob := false
		for event := range watcher.ResultChan() {
			job = event.Object.(*batchv1.Job)
			logr.FromContextOrDiscard(ctx).Info("watch event", "job", job)

			if len(image) > 0 {
				if job.Spec.Template.Spec.Containers[0].Image != image {
					logr.FromContextOrDiscard(ctx).Info("expected job image doesn't match")
					continue
				}
			}

			if !strings.Contains(strings.Join(job.Spec.Template.Spec.Containers[0].Command, " "), migration) {
				logr.FromContextOrDiscard(ctx).Info("expected job migration doesn't match")
				continue
			}
			if phase != "" {
				foundPhase := false
				for _, e := range job.Spec.Template.Spec.Containers[0].Env {
					logr.FromContextOrDiscard(ctx).Info("env var", "value", e)
					if e.Value == phase {
						foundPhase = true
					}
				}
				if !foundPhase {
					logr.FromContextOrDiscard(ctx).Info("expected job phase doesn't match")
					continue
				}
			}

			// found a matching job
			TailF(job)

			// wait for job to succeed
			if job.Status.Succeeded == 0 {
				logr.FromContextOrDiscard(ctx).Info("job hasn't succeeded")
				continue
			}
			matchingJob = true
			break
		}
		Expect(matchingJob).To(BeTrue())
	}
}
