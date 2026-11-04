// Package metrics 提供每个进程的内观测听 HTTP 端口：
//
//	/healthz         存活探针（进程活着即 200）
//	/readyz          就绪探针（依赖就绪后 200，可通过 SetReady 控制）
//	/metrics         Prometheus 指标
//	/debug/pprof     Go 性能剖析
//
// gateway / gameserver / admin 各自可起一个（默认用独立端口）。标准 launcher
// 只启动 gateway、gameserver；admin handler 内嵌在 launcher 中。
package metrics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Server struct {
	srv *http.Server
	reg *prometheus.Registry

	ready   bool
	started time.Time
}

// NewServer 构建观测端口。name 用于进程标签（gateway/gameserver/admin）。
func NewServer(addr, name string) *Server {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	buildInfo := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "rf4_build_info",
		Help:        "构建信息",
		ConstLabels: prometheus.Labels{"process": name},
	})
	buildInfo.Set(1)
	reg.MustRegister(buildInfo)

	s := &Server{reg: reg, started: time.Now()}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.ready {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready\n"))
	})
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	s.srv = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

// Registry 返回本进程指标注册表（业务包把自定义指标注册进来）。
func (s *Server) Registry() *prometheus.Registry { return s.reg }

// SetReady 控制就绪探针。
func (s *Server) SetReady(v bool) { s.ready = v }

// Start 异步启动监听；返回的 channel 在监听失败时收到错误。
func (s *Server) Start() <-chan error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("观测端口监听失败: %w", err)
		}
	}()
	return errCh
}

// Shutdown 优雅停机。
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
