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

const PGAdminConnString = "postgresql://postgres:testpassword@localhost:%d/postgres?sslmode=disable"

type PostgresProvider struct {
	kclient    kubernetes.Interface
	namespace  string
	lock       lockfile.Lockfile
	mapper     meta.RESTMapper
	restConfig *rest.Config
}

func NewPostgresProvider(mapper meta.RESTMapper, restConfig *rest.Config, namespace string) *PostgresProvider {
	k, err := kubernetes.NewForConfig(restConfig)
	Expect(err).To(Succeed())
	lockPath, err := filepath.Abs("./postgres.lck")
	Expect(err).To(Succeed())
	lock, err := lockfile.New(lockPath)
	Expect(err).To(Succeed())
	return &PostgresProvider{kclient: k, namespace: namespace, lock: lock, mapper: mapper, restConfig: restConfig}
}

func (p *PostgresProvider) New(ctx context.Context) *LogicalDatabase {
	p.ensureDatabase(ctx)
	var logicalDBName string
	Eventually(func(g Gomega) {
		logicalDBName = names.SimpleNameGenerator.GenerateName("pg")
		p.exec(ctx, g, fmt.Sprintf("CREATE DATABASE %s;", logicalDBName))
	}).Should(Succeed())
	return &LogicalDatabase{
		DatastoreURI: fmt.Sprintf("postgresql://postgres:testpassword@postgresql-db-public.%s:5432/%s?sslmode=disable", p.namespace, logicalDBName),
		DatabaseName: logicalDBName,
		Engine:       "postgres",
	}
}

func (p *PostgresProvider) Cleanup(ctx context.Context, db *LogicalDatabase) {
	p.exec(ctx, Default, fmt.Sprintf("DROP DATABASE %s;", db.DatabaseName))
}

func (p *PostgresProvider) exec(ctx context.Context, g Gomega, cmd string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	ports := e2eutil.PortForward(g, p.namespace, "postgresql-db-0", []string{":5432"}, ctx.Done())
	g.Expect(len(ports)).To(Equal(1))
	conn, err := pgx.Connect(ctx, fmt.Sprintf(PGAdminConnString, ports[0].Local))
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

// ensureDatabase checks to see if the postgres instance has been set up,
// and if not, creates it under a lock
func (p *PostgresProvider) ensureDatabase(ctx context.Context) {
	logger := logr.FromContextOrDiscard(ctx)

	for {
		if p.running(ctx) == nil {
			logger.V(4).Info("pg found running")
			return
		}
		logger.V(2).Info("pg not running, attempting to acquire lock")
		if err := p.lock.TryLock(); err != nil {
			logger.V(2).Info("cannot acquire lock, retry")
			continue
		}
		defer func() {
			Expect(p.lock.Unlock()).To(Succeed())
			logger.V(4).Info("pg lock released, creating database")
		}()
		logger.V(2).Info("pg lock acquired, creating database")

		CreateFromManifests(ctx, p.namespace, "postgres", p.restConfig, p.mapper)

		Eventually(func(g Gomega) {
			g.Expect(p.running(ctx)).To(Succeed())
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			p.exec(ctx, g, "ALTER ROLE postgres CONNECTION LIMIT -1;")
		}).Should(Succeed())
		return
	}
}

func (p *PostgresProvider) running(ctx context.Context) error {
	if _, err := p.kclient.CoreV1().Namespaces().Get(ctx, p.namespace, metav1.GetOptions{}); err != nil {
		return err
	}

	if _, err := p.kclient.AppsV1().StatefulSets(p.namespace).Get(ctx, "postgresql-db", metav1.GetOptions{}); err != nil {
		return err
	}

	return nil
}
