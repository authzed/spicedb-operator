package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/component-base/metrics"

	ctrlmetrics "github.com/authzed/spicedb-operator/pkg/libctrl/metrics"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

var ClusterMetrics = &ctrlmetrics.ConditionStatusCollector[*v1alpha1.SpiceDBCluster]{
	ObjectCount: metrics.NewDesc(
		prometheus.BuildFQName("spicedb_operator", "clusters", "count"),
		"Gauge showing the number of clusters managed by this operator",
		nil, nil, metrics.ALPHA, "",
	),
	ObjectConditionCount: metrics.NewDesc(
		prometheus.BuildFQName("spicedb_operator", "clusters", "condition_count"),
		"Gauge showing the number of clusters with each type of condition",
		[]string{
			"condition",
		}, nil, metrics.ALPHA, "",
	),
	ObjectTimeInCondition: metrics.NewDesc(
		prometheus.BuildFQName("spicedb_operator", "clusters", "condition_time_seconds"),
		"Gauge showing the amount of time the cluster has spent in the current condition",
		[]string{
			"condition",
			"cluster",
		}, nil, metrics.ALPHA, "",
	),
	CollectorTime: metrics.NewDesc(
		prometheus.BuildFQName("spicedb_operator", "cluster_status_collector", "execution_seconds"),
		"Amount of time spent on the last run of the cluster status collector",
		nil, nil, metrics.ALPHA, "",
	),
	CollectorErrors: metrics.NewDesc(
		prometheus.BuildFQName("spicedb_operator", "cluster_status_collector", "errors_count"),
		"Number of errors encountered on the last run of the cluster status collector",
		nil, nil, metrics.ALPHA, "",
	),
}

var _ metrics.StableCollector = &ctrlmetrics.ConditionStatusCollector[*v1alpha1.SpiceDBCluster]{}
