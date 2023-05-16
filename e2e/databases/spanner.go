package databases

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	database "cloud.google.com/go/spanner/admin/database/apiv1"
	instances "cloud.google.com/go/spanner/admin/instance/apiv1"
	"github.com/go-logr/logr"
	"github.com/nightlyone/lockfile"

	//revive:disable:dot-imports convention is dot-import
	. "github.com/onsi/gomega"
	"google.golang.org/api/option"
	adminpb "google.golang.org/genproto/googleapis/spanner/admin/database/v1"
	"google.golang.org/genproto/googleapis/spanner/admin/instance/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	e2eutil "github.com/authzed/spicedb-operator/e2e/util"
)

type SpannerProvider struct {
	kclient    kubernetes.Interface
	namespace  string
	lock       lockfile.Lockfile
	mapper     meta.RESTMapper
	restConfig *rest.Config
}

func NewSpannerProvider(mapper meta.RESTMapper, restConfig *rest.Config, namespace string) *SpannerProvider {
	k, err := kubernetes.NewForConfig(restConfig)
	Expect(err).To(Succeed())
	lockPath, err := filepath.Abs("./spanner.lck")
	Expect(err).To(Succeed())
	lock, err := lockfile.New(lockPath)
	Expect(err).To(Succeed())
	return &SpannerProvider{kclient: k, namespace: namespace, lock: lock, mapper: mapper, restConfig: restConfig}
}

func (p *SpannerProvider) New(ctx context.Context) *LogicalDatabase {
	p.ensureDatabase(ctx)
	var logicalDBName string

	Eventually(func(g Gomega) {
		logicalDBName = names.SimpleNameGenerator.GenerateName("sp")
		p.execDatabase(ctx, g, func(client *database.DatabaseAdminClient) {
			op, err := client.CreateDatabase(ctx, &adminpb.CreateDatabaseRequest{
				Parent:          "projects/fake-project-id/instances/fake-instance",
				CreateStatement: "CREATE DATABASE `" + logicalDBName + "`",
			})
			g.Expect(err).To(Succeed())

			_, err = op.Wait(ctx)
			g.Expect(err).To(Succeed())
		})
	}).Should(Succeed())
	return &LogicalDatabase{
		DatastoreURI: fmt.Sprintf("projects/fake-project-id/instances/fake-instance/databases/%s", logicalDBName),
		DatabaseName: logicalDBName,
		ExtraConfig: map[string]string{
			"datastoreSpannerEmulatorHost": fmt.Sprintf("spanner-service.%s:9010", p.namespace),
		},
		Engine: "spanner",
	}
}

func (p *SpannerProvider) Cleanup(_ context.Context, _ *LogicalDatabase) {
	// TODO: figure out how to cleanup a spanner emulator db
}

func (p *SpannerProvider) execInstance(ctx context.Context, g Gomega, cmd func(client *instances.InstanceAdminClient)) {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Second)
	defer cancel()
	ports := e2eutil.PortForward(Default, p.namespace, "spanner-0", []string{":9010"}, ctx.Done())
	g.Expect(len(ports)).To(Equal(1))

	Expect(os.Setenv("SPANNER_EMULATOR_HOST", fmt.Sprintf("localhost:%d", ports[0].Local))).To(Succeed())

	var instancesClient *instances.InstanceAdminClient
	g.Eventually(func() error {
		var err error
		instancesClient, err = instances.NewInstanceAdminClient(ctx,
			option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
			option.WithoutAuthentication(),
			option.WithEndpoint(fmt.Sprintf("localhost:%d", ports[0].Local)))
		return err
	}).Should(Succeed())
	defer func() { Expect(instancesClient.Close()).To(Succeed()) }()

	cmd(instancesClient)
}

func (p *SpannerProvider) execDatabase(ctx context.Context, g Gomega, cmd func(client *database.DatabaseAdminClient)) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ports := e2eutil.PortForward(Default, p.namespace, "spanner-0", []string{":9010"}, ctx.Done())
	g.Expect(len(ports)).To(Equal(1))

	adminClient, err := database.NewDatabaseAdminClient(ctx,
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
		option.WithoutAuthentication(),
		option.WithEndpoint(fmt.Sprintf("localhost:%d", ports[0].Local)))
	Expect(err).To(Succeed())
	defer func() {
		Expect(adminClient.Close()).To(Succeed())
	}()

	cmd(adminClient)
}

// ensureDatabase checks to see if the spanner instance has been set up,
// and if not, creates it under a lock
func (p *SpannerProvider) ensureDatabase(ctx context.Context) {
	logger := logr.FromContextOrDiscard(ctx)

	for {
		if p.running(ctx) == nil {
			logger.V(4).Info("spanner found running")
			return
		}
		logger.V(2).Info("spanner not running, attempting to acquire lock")
		if err := p.lock.TryLock(); err != nil {
			logger.V(2).Info("cannot acquire lock, retry")
			continue
		}
		defer func() {
			Expect(p.lock.Unlock()).To(Succeed())
			logger.V(4).Info("spanner lock released, creating database")
		}()
		logger.V(2).Info("spanner lock acquired, creating database")

		CreateFromManifests(ctx, p.namespace, "spanner", p.restConfig, p.mapper)

		Eventually(func(g Gomega) {
			p.execInstance(ctx, g, func(client *instances.InstanceAdminClient) {
				createInstanceOp, err := client.CreateInstance(ctx, &instance.CreateInstanceRequest{
					Parent:     "projects/fake-project-id",
					InstanceId: "fake-instance",
					Instance: &instance.Instance{
						Config:      "emulator-config",
						DisplayName: "Test Instance",
						NodeCount:   1,
					},
				})
				g.Expect(err).To(Succeed())
				_, err = createInstanceOp.Wait(ctx)
				g.Expect(err).To(Succeed())
			})
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(p.running(ctx)).To(Succeed())
		})
		return
	}
}

func (p *SpannerProvider) running(ctx context.Context) error {
	if _, err := p.kclient.CoreV1().Namespaces().Get(ctx, p.namespace, metav1.GetOptions{}); err != nil {
		return err
	}

	if _, err := p.kclient.AppsV1().StatefulSets(p.namespace).Get(ctx, "spanner", metav1.GetOptions{}); err != nil {
		return err
	}

	var err error
	p.execInstance(ctx, Default, func(client *instances.InstanceAdminClient) {
		var i *instance.Instance
		i, err = client.GetInstance(ctx, &instance.GetInstanceRequest{
			Name: "projects/fake-project-id/instances/fake-instance",
		})
		fmt.Println(i)
	})
	return err
}
