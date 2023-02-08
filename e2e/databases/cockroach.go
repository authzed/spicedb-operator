package databases

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/go-logr/logr"
	"github.com/jackc/pgx/v5"
	"github.com/nightlyone/lockfile"

	//revive:disable:dot-imports convention is dot-import
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	e2eutil "github.com/authzed/spicedb-operator/e2e/util"
)

const CRDBAdminConnString = "postgresql://root:unused@localhost:%d/defaultdb?sslmode=disable"

type CockroachProvider struct {
	kclient    kubernetes.Interface
	namespace  string
	lock       lockfile.Lockfile
	mapper     meta.RESTMapper
	restConfig *rest.Config
}

func NewCockroachProvider(mapper meta.RESTMapper, restConfig *rest.Config, namespace string) *CockroachProvider {
	k, err := kubernetes.NewForConfig(restConfig)
	Expect(err).To(Succeed())
	lockPath, err := filepath.Abs("./crdb.lck")
	Expect(err).To(Succeed())
	lock, err := lockfile.New(lockPath)
	Expect(err).To(Succeed())
	return &CockroachProvider{kclient: k, namespace: namespace, lock: lock, mapper: mapper, restConfig: restConfig}
}

func (p *CockroachProvider) New(ctx context.Context) *LogicalDatabase {
	p.ensureDatabase(ctx)
	var logicalDBName string
	Eventually(func(g Gomega) {
		logicalDBName = names.SimpleNameGenerator.GenerateName("crdb")
		p.exec(ctx, g, fmt.Sprintf("CREATE DATABASE %s;", logicalDBName))
	}).Should(Succeed())
	return &LogicalDatabase{
		DatastoreURI: fmt.Sprintf("postgresql://root:unused@cockroachdb-public.%s:26257/%s?sslmode=disable", p.namespace, logicalDBName),
		DatabaseName: logicalDBName,
		Engine:       "cockroachdb",
	}
}

func (p *CockroachProvider) Cleanup(ctx context.Context, db *LogicalDatabase) {
	p.exec(ctx, Default, fmt.Sprintf("DROP DATABASE %s;", db.DatabaseName))
}

func (p *CockroachProvider) exec(ctx context.Context, g Gomega, cmd string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	ports := e2eutil.PortForward(g, p.namespace, "cockroachdb-0", []string{":26257"}, ctx.Done())
	g.Expect(len(ports)).To(Equal(1))
	conn, err := pgx.Connect(ctx, fmt.Sprintf(CRDBAdminConnString, ports[0].Local))
	if err != nil {
		GinkgoWriter.Println(err)
	}

	g.Expect(err).To(Succeed())
	_, err = conn.Exec(ctx, cmd)
	if err != nil {
		GinkgoWriter.Println(err)
	}
	g.Expect(err).To(Succeed())
}

// ensureDatabase checks to see if the cockroach instance has been set up,
// and if not, creates it under a lock
func (p *CockroachProvider) ensureDatabase(ctx context.Context) {
	logger := logr.FromContextOrDiscard(ctx)

	for {
		if p.running(ctx) == nil {
			logger.V(4).Info("crdb found running")
			return
		}
		logger.V(2).Info("crdb not running, attempting to acquire lock")
		if err := p.lock.TryLock(); err != nil {
			logger.V(2).Info("cannot acquire lock, retry")
			continue
		}
		defer func() {
			Expect(p.lock.Unlock()).To(Succeed())
			logger.V(4).Info("crdb lock released, creating database")
		}()
		logger.V(2).Info("crdb lock acquired, creating database")

		CreateFromManifests(ctx, p.namespace, "cockroachdb", p.restConfig, p.mapper)

		Eventually(func(g Gomega) {
			g.Expect(p.running(ctx)).To(Succeed())
		}).Should(Succeed())
		return
	}
}

func (p *CockroachProvider) running(ctx context.Context) error {
	if _, err := p.kclient.CoreV1().Namespaces().Get(ctx, p.namespace, metav1.GetOptions{}); err != nil {
		return err
	}

	if _, err := p.kclient.AppsV1().StatefulSets(p.namespace).Get(ctx, "cockroachdb", metav1.GetOptions{}); err != nil {
		return err
	}

	return nil
}
