package controller

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/component-base/metrics"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

var ClusterMetrics = &spicedbClusterStatusCollector{
	clusterCount: metrics.NewDesc(
		prometheus.BuildFQName("spicedb_operator", "clusters", "count"),
		"Gauge showing the number of clusters managed by this operator",
		nil, nil, metrics.ALPHA, "",
	),
	clusterConditionCount: metrics.NewDesc(
		prometheus.BuildFQName("spicedb_operator", "clusters", "condition_count"),
		"Gauge showing the number of clusters with each type of condition",
		[]string{
			"condition",
		}, nil, metrics.ALPHA, "",
	),
	clusterTimeInCondition: metrics.NewDesc(
		prometheus.BuildFQName("spicedb_operator", "clusters", "condition_time_seconds"),
		"Gauge showing the amount of time the cluster has spent in the current condition",
		[]string{
			"condition",
			"cluster",
		}, nil, metrics.ALPHA, "",
	),
	collectorTime: metrics.NewDesc(
		prometheus.BuildFQName("spicedb_operator", "cluster_status_collector", "execution_seconds"),
		"Amount of time spent on the last run of the cluster status collector",
		nil, nil, metrics.ALPHA, "",
	),
	collectorErrors: metrics.NewDesc(
		prometheus.BuildFQName("spicedb_operator", "cluster_status_collector", "errors_count"),
		"Number of errors encountered on the last run of the cluster status collector",
		nil, nil, metrics.ALPHA, "",
	),
}

type spicedbClusterStatusCollector struct {
	metrics.BaseStableCollector

	listerBuilders []clusterListerBuilder

	clusterCount           *metrics.Desc
	clusterConditionCount  *metrics.Desc
	clusterTimeInCondition *metrics.Desc
	collectorTime          *metrics.Desc
	collectorErrors        *metrics.Desc
}

type clusterListerBuilder func() ([]v1alpha1.SpiceDBCluster, error)

func (c *spicedbClusterStatusCollector) addClusterListerBuilder(lb clusterListerBuilder) {
	c.listerBuilders = append(c.listerBuilders, lb)
}

func (c *spicedbClusterStatusCollector) DescribeWithStability(ch chan<- *metrics.Desc) {
	ch <- c.clusterCount
	ch <- c.clusterConditionCount
	ch <- c.clusterTimeInCondition
	ch <- c.collectorTime
	ch <- c.collectorErrors
}

func (c *spicedbClusterStatusCollector) CollectWithStability(ch chan<- metrics.Metric) {
	start := time.Now()
	totalErrors := 0
	defer func() {
		duration := time.Since(start)

		ch <- metrics.NewLazyConstMetric(c.collectorTime, metrics.GaugeValue, duration.Seconds())
		ch <- metrics.NewLazyConstMetric(c.collectorErrors, metrics.GaugeValue, float64(totalErrors))
	}()

	collectTime := time.Now()

	for _, lb := range c.listerBuilders {
		clusters, err := lb()
		if err != nil {
			totalErrors++
			continue
		}

		ch <- metrics.NewLazyConstMetric(c.clusterCount, metrics.GaugeValue, float64(len(clusters)))

		clustersWithCondition := map[string]uint16{}
		for _, cluster := range clusters {
			clusterName := cluster.NamespacedName().String()
			for _, condition := range cluster.Status.Conditions {
				clustersWithCondition[condition.Type]++

				timeInCondition := collectTime.Sub(condition.LastTransitionTime.Time)

				ch <- metrics.NewLazyConstMetric(
					c.clusterTimeInCondition,
					metrics.GaugeValue,
					timeInCondition.Seconds(),
					condition.Type,
					clusterName,
				)
			}
		}

		for conditionType, count := range clustersWithCondition {
			ch <- metrics.NewLazyConstMetric(c.clusterConditionCount, metrics.GaugeValue, float64(count), conditionType)
		}
	}
}

var _ metrics.StableCollector = &spicedbClusterStatusCollector{}
