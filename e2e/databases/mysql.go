package databases

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"github.com/go-logr/logr"
	sqlDriver "github.com/go-sql-driver/mysql"
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

const MySQLAdminConnString = "root:password@tcp(localhost:%d)/mysql?parseTime=true"

type MySQLProvider struct {
	kclient    kubernetes.Interface
	namespace  string
	lock       lockfile.Lockfile
	mapper     meta.RESTMapper
	restConfig *rest.Config
}

func NewMySQLProvider(mapper meta.RESTMapper, restConfig *rest.Config, namespace string) *MySQLProvider {
	k, err := kubernetes.NewForConfig(restConfig)
	Expect(err).To(Succeed())
	lockPath, err := filepath.Abs("./msql.lck")
	Expect(err).To(Succeed())
	lock, err := lockfile.New(lockPath)
	Expect(err).To(Succeed())
	return &MySQLProvider{kclient: k, namespace: namespace, lock: lock, mapper: mapper, restConfig: restConfig}
}

func (p *MySQLProvider) New(ctx context.Context) *LogicalDatabase {
	p.ensureDatabase(ctx)
	var logicalDBName string
	Eventually(func(g Gomega) {
		logicalDBName = names.SimpleNameGenerator.GenerateName("my")
		p.exec(ctx, g, fmt.Sprintf("CREATE DATABASE %s;", logicalDBName))
	}).Should(Succeed())
	return &LogicalDatabase{
		DatastoreURI: fmt.Sprintf("root:password@tcp(mysql-public.%s:3306)/%s?parseTime=true", p.namespace, logicalDBName),
		DatabaseName: logicalDBName,
		Engine:       "mysql",
	}
}

func (p *MySQLProvider) Cleanup(ctx context.Context, db *LogicalDatabase) {
	p.exec(ctx, Default, fmt.Sprintf("DROP DATABASE %s;", db.DatabaseName))
}

func (p *MySQLProvider) exec(ctx context.Context, g Gomega, cmd string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	ports := e2eutil.PortForward(g, p.namespace, "mysql-0", []string{":3306"}, ctx.Done())
	g.Expect(len(ports)).To(Equal(1))

	dbConfig, err := sqlDriver.ParseDSN(fmt.Sprintf(MySQLAdminConnString, ports[0].Local))
	if err != nil {
		GinkgoWriter.Println(err)
	}
	g.Expect(err).To(Succeed())

	db, err := sql.Open("mysql", dbConfig.FormatDSN())
	if err != nil {
		GinkgoWriter.Println(err)
	}
	g.Expect(err).To(Succeed())

	_, err = db.ExecContext(ctx, cmd)
	if err != nil {
		GinkgoWriter.Println(err)
	}
	g.Expect(err).To(Succeed())
}

// ensureDatabase checks to see if the mysql instance has been set up,
// and if not, creates it under a lock
func (p *MySQLProvider) ensureDatabase(ctx context.Context) {
	logger := logr.FromContextOrDiscard(ctx)

	for {
		if p.running(ctx) == nil {
			logger.V(4).Info("mysql found running")
			return
		}
		logger.V(2).Info("mysql not running, attempting to acquire lock")
		if err := p.lock.TryLock(); err != nil {
			logger.V(2).Info("cannot acquire lock, retry")
			continue
		}
		defer func() {
			Expect(p.lock.Unlock()).To(Succeed())
			logger.V(4).Info("mysql lock released, creating database")
		}()
		logger.V(2).Info("mysql lock acquired, creating database")

		CreateFromManifests(ctx, p.namespace, "mysql", p.restConfig, p.mapper)

		Eventually(func(g Gomega) {
			g.Expect(p.running(ctx)).To(Succeed())
		}).Should(Succeed())
		return
	}
}

func (p *MySQLProvider) running(ctx context.Context) error {
	if _, err := p.kclient.CoreV1().Namespaces().Get(ctx, p.namespace, metav1.GetOptions{}); err != nil {
		return err
	}

	if _, err := p.kclient.AppsV1().StatefulSets(p.namespace).Get(ctx, "mysql", metav1.GetOptions{}); err != nil {
		return err
	}

	return nil
}
