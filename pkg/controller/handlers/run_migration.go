package handlers

import (
	"context"
	"crypto/subtle"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	applybatchv1 "k8s.io/client-go/applyconfigurations/batch/v1"
	"k8s.io/klog/v2"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/controller/handlercontext"
	"github.com/authzed/spicedb-operator/pkg/libctrl"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

// TODO: see if the config hashing can be generalized / unified with the object hashing
type MigrationRunHandler struct {
	libctrl.ControlAll
	patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	applyJob    func(ctx context.Context, job *applybatchv1.JobApplyConfiguration) error
	deleteJob   func(ctx context.Context, name string) error
	next        handler.ContextHandler
}

func NewMigrationRunHandler(ctrls libctrl.HandlerControls,
	patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error,
	applyJob func(ctx context.Context, job *applybatchv1.JobApplyConfiguration) error,
	deleteJob func(ctx context.Context, name string) error,
	next handler.ContextHandler,
) handler.Handler {
	return handler.NewHandler(&MigrationRunHandler{
		ControlAll: ctrls,
		applyJob: func(ctx context.Context, job *applybatchv1.JobApplyConfiguration) error {
			klog.V(4).InfoS("creating migration job", "namespace", *job.Namespace, "name", *job.Name)
			return applyJob(ctx, job)
		},
		deleteJob: func(ctx context.Context, name string) error {
			klog.V(4).InfoS("deleting migration job", "namespace", handlercontext.CtxClusterNN.MustValue(ctx).Namespace, "name", name)
			return deleteJob(ctx, name)
		},
		patchStatus: patchStatus,
		next:        next,
	}, "createMigrationJob")
}

func (m *MigrationRunHandler) Handle(ctx context.Context) {
	// TODO: setting status is unconditional, should happen in a separate handler
	currentStatus := handlercontext.CtxClusterStatus.MustValue(ctx)
	config := handlercontext.CtxConfig.MustValue(ctx)
	currentStatus.SetStatusCondition(v1alpha1.NewMigratingCondition(config.DatastoreEngine, "head"))
	if err := m.patchStatus(ctx, currentStatus); err != nil {
		m.RequeueErr(err)
		return
	}
	ctx = handlercontext.CtxClusterStatus.WithValue(ctx, currentStatus)

	jobs := handlercontext.CtxJobs.MustValue(ctx)
	migrationHash := handlercontext.CtxMigrationHash.Value(ctx)

	matchingObjs := make([]*batchv1.Job, 0)
	extraObjs := make([]*batchv1.Job, 0)
	for _, o := range jobs {
		annotations := o.GetAnnotations()
		if annotations == nil {
			extraObjs = append(extraObjs, o)
		}
		if subtle.ConstantTimeCompare([]byte(annotations[metadata.SpiceDBMigrationRequirementsKey]), []byte(migrationHash)) == 1 {
			matchingObjs = append(matchingObjs, o)
		} else {
			extraObjs = append(extraObjs, o)
		}
	}

	if len(matchingObjs) == 0 {
		// apply if no matching object in controller
		err := m.applyJob(ctx, handlercontext.CtxConfig.MustValue(ctx).MigrationJob(migrationHash))
		if err != nil {
			m.RequeueAPIErr(err)
			return
		}
	}

	// delete extra objects
	for _, o := range extraObjs {
		if err := m.deleteJob(ctx, o.GetName()); err != nil {
			m.RequeueAPIErr(err)
			return
		}
	}

	// job with correct hash exists
	if len(matchingObjs) >= 1 {
		ctx = handlercontext.CtxCurrentMigrationJob.WithValue(ctx, matchingObjs[0])
		m.next.Handle(ctx)
		return
	}

	// if we had to create a job, requeue after a wait since the job takes time
	m.RequeueAfter(5 * time.Second)
}
