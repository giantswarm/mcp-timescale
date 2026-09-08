package server

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricsNamespace is the Prometheus metric prefix. Override per-server by
// re-declaring metrics on a different registry.
const MetricsNamespace = "mcp"

// Registry is the package-local Prometheus registry. Owned (rather than
// using prometheus.DefaultRegisterer) so tests can construct the server
// twice without duplicate-registration panics.
var Registry = func() *prometheus.Registry {
	r := prometheus.NewRegistry()
	r.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return r
}()

// MetricsHandler serves /metrics from Registry in Prometheus text format.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{})
}
