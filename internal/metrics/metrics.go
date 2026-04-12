// Package metrics exposes the Prometheus collectors for electrum-go.
//
// All collectors are package-level so that instrumentation sites can simply
// call e.g. metrics.BlocksIndexed.Inc() without dependency plumbing. When the
// metrics endpoint is disabled (the default), collectors are still allocated
// and safe to mutate — they just aren't registered into the exporter registry
// and no HTTP server is started.
package metrics

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	ChainHeight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "electrum_go_chain_height",
		Help: "Currently indexed chain tip height.",
	})

	BitcoinCoreHeight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "electrum_go_bitcoin_core_height",
		Help: "Last-seen bitcoind tip height from periodic checks.",
	})

	MempoolTxCount = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "electrum_go_mempool_tx_count",
		Help: "Current number of transactions in the mempool overlay.",
	})

	ActiveConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "electrum_go_active_connections",
		Help: "Currently open Electrum client connections.",
	})

	Subscriptions = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "electrum_go_subscriptions",
			Help: "Active subscriptions by type (scripthash, header, conn).",
		},
		[]string{"type"},
	)

	DBSizeBytes = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "electrum_go_db_size_bytes",
		Help: "Pebble database on-disk size in bytes (sampled periodically).",
	})

	Info = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "electrum_go_info",
			Help: "Build and runtime metadata; always set to 1.",
		},
		[]string{"version", "commit", "chain"},
	)

	BlocksIndexed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "electrum_go_blocks_indexed_total",
		Help: "Total blocks successfully indexed.",
	})

	MempoolTxsReceived = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "electrum_go_mempool_txs_received_total",
		Help: "Total mempool transactions received from ZMQ.",
	})

	MempoolTxsIndexed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "electrum_go_mempool_txs_indexed_total",
		Help: "Total mempool transactions successfully added to the overlay.",
	})

	Reorgs = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "electrum_go_reorgs_total",
		Help: "Total chain reorganizations detected.",
	})

	ZMQSeqGaps = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "electrum_go_zmq_seq_gaps_total",
			Help: "Total ZMQ sequence gaps detected, by stream.",
		},
		[]string{"stream"},
	)

	BitcoinRPCErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "electrum_go_bitcoin_rpc_errors_total",
		Help: "Total errors returned by bitcoind RPC calls.",
	})

	ElectrumRPCDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "electrum_go_electrum_rpc_duration_seconds",
			Help:    "Electrum method handler latency in seconds, by method.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method"},
	)

	BlockIndexDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "electrum_go_block_index_duration_seconds",
		Help:    "Time to index a single block in seconds.",
		Buckets: prometheus.DefBuckets,
	})

	BitcoinRPCDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "electrum_go_bitcoin_rpc_duration_seconds",
			Help:    "bitcoind RPC call latency in seconds, by method.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method"},
	)
)

var registry = prometheus.NewRegistry()

// Init registers all collectors into the exporter registry and sets the info
// gauge. Call exactly once at startup when metrics are enabled.
func Init(chain, version, commit string) {
	collectors := []prometheus.Collector{
		ChainHeight, BitcoinCoreHeight, MempoolTxCount, ActiveConnections,
		Subscriptions, DBSizeBytes, Info,
		BlocksIndexed, MempoolTxsReceived, MempoolTxsIndexed, Reorgs,
		ZMQSeqGaps, BitcoinRPCErrors,
		ElectrumRPCDuration, BlockIndexDuration, BitcoinRPCDuration,
	}
	for _, c := range collectors {
		registry.MustRegister(c)
	}
	Info.WithLabelValues(version, commit, chain).Set(1)
}

// ServeMetrics starts an HTTP server exposing /metrics on addr. It blocks and
// should be invoked in a goroutine. Returns the listener error on shutdown.
func ServeMetrics(addr string) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("📈 Metrics endpoint listening on http://%s/metrics", addr)
	return srv.Serve(ln)
}

// StartDBSizeSampler launches a background goroutine that sums the on-disk
// size of dbPath every interval and updates DBSizeBytes. The goroutine exits
// when ctx is cancelled. Sampling cost is O(files) but /metrics scrapes stay
// O(1) because the handler just reads the gauge.
func StartDBSizeSampler(ctx context.Context, dbPath string, interval time.Duration) {
	sample := func() {
		var total int64
		err := filepath.Walk(dbPath, func(_ string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if !info.IsDir() {
				total += info.Size()
			}
			return nil
		})
		if err == nil {
			DBSizeBytes.Set(float64(total))
		}
	}

	go func() {
		sample()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sample()
			}
		}
	}()
}

// ObserveBitcoinRPC records a bitcoind RPC call's duration (and failure, if
// err is non-nil) for the given method name. Call it after each instrumented
// rpcclient call.
func ObserveBitcoinRPC(method string, start time.Time, err error) {
	BitcoinRPCDuration.WithLabelValues(method).Observe(time.Since(start).Seconds())
	if err != nil {
		BitcoinRPCErrors.Inc()
	}
}
