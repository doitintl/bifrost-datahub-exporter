package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/doitintl/bifrost-datahub-exporter/internal/bifrost"
	"github.com/doitintl/bifrost-datahub-exporter/internal/config"
	"github.com/doitintl/bifrost-datahub-exporter/internal/datahub"
	"github.com/doitintl/bifrost-datahub-exporter/internal/metrics"
	"github.com/doitintl/bifrost-datahub-exporter/internal/runner"
)

var version = "dev"

func main() {
	once := flag.Bool("once", false, "run a single export cycle and exit (for cron or verification)")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("version", version)

	cfg, err := config.FromEnv()
	if err != nil {
		log.Error("configuration error", "err", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	bc := bifrost.NewClient(cfg.BifrostBaseURL, cfg.BifrostAdminUsername, cfg.BifrostAdminPassword)
	dc := datahub.NewClient(cfg.DoiTAPIURL, cfg.DoiTAPIKey, "bifrost-datahub-exporter/"+version, log)
	r := runner.New(cfg, bc, dc, log)

	if err := r.Startup(ctx); err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}

	if *once {
		if err := r.Cycle(ctx); err != nil {
			log.Error("cycle failed", "err", err)
			os.Exit(1)
		}

		return
	}

	metrics.Serve(cfg.MetricsAddr)
	log.Info("exporter started", "mode", cfg.Mode, "dataset", cfg.Dataset, "poll_interval", cfg.PollInterval, "metrics", cfg.MetricsAddr)

	if err := r.Run(ctx); err != nil && ctx.Err() == nil {
		log.Error("runner stopped", "err", err)
		os.Exit(1)
	}
}
