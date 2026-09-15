package runner

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/doitintl/bifrost-datahub-exporter/internal/bifrost"
	"github.com/doitintl/bifrost-datahub-exporter/internal/config"
	"github.com/doitintl/bifrost-datahub-exporter/internal/datahub"
	"github.com/doitintl/bifrost-datahub-exporter/internal/mapper"
	"github.com/doitintl/bifrost-datahub-exporter/internal/metrics"
	"github.com/doitintl/bifrost-datahub-exporter/internal/state"
)

type Runner struct {
	cfg     config.Config
	bifrost *bifrost.Client
	datahub *datahub.Client
	log     *slog.Logger
}

func New(cfg config.Config, bc *bifrost.Client, dc *datahub.Client, log *slog.Logger) *Runner {
	return &Runner{cfg: cfg, bifrost: bc, datahub: dc, log: log}
}

// Startup probes the gateway and ensures the dataset exists with its
// description (and preset logo where the DoiT side supports it).
func (r *Runner) Startup(ctx context.Context) error {
	caps, err := r.bifrost.Probe(ctx)
	if err != nil {
		return fmt.Errorf("gateway probe (GET /api/logs): %w", err)
	}

	r.log.Info("gateway probed", "logs_total", caps.TotalLogs, "cost_split", caps.CostSplit)

	if caps.TotalLogs > 0 && !caps.CostSplit {
		r.log.Warn("log rows carry no token_usage.cost split — gateway likely predates transports v2.0.0; Input/Output Cost metrics will be absent (best-effort support)")
	}

	if err := r.datahub.EnsureDataset(ctx, r.cfg.Dataset, r.cfg.DatasetDescription(), r.cfg.DatasetLogoName); err != nil {
		return err
	}

	r.log.Info("dataset ensured", "dataset", r.cfg.Dataset)

	return nil
}

// Run executes cycles until ctx is done. Every cycle re-reads a trailing
// window (checkpoint- and lookback-based): Bifrost writes log rows
// asynchronously after the response, finalizes "processing" rows later,
// and can restate cost after pricing changes — deterministic ids make the
// overlap an idempotent overwrite on the DataHub side.
func (r *Runner) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()

	for {
		if err := r.Cycle(ctx); err != nil {
			metrics.CycleErrors.Add(1)
			r.log.Error("cycle failed", "err", err)
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (r *Runner) Cycle(ctx context.Context) error {
	metrics.Cycles.Add(1)

	now := time.Now().UTC()
	from := now.Add(-time.Duration(r.cfg.LookbackHours) * time.Hour)

	st := state.Load(r.cfg.StateFile)
	if st.LastSuccess.IsZero() && r.cfg.BackfillDays > 0 {
		from = now.AddDate(0, 0, -r.cfg.BackfillDays)
		r.log.Info("no checkpoint, backfilling", "days", r.cfg.BackfillDays)
	} else if !st.LastSuccess.IsZero() && st.LastSuccess.Before(from) {
		from = st.LastSuccess.Add(-time.Hour)
		r.log.Info("checkpoint older than lookback, widening window", "from", from)
	}

	rows, err := r.bifrost.Logs(ctx, from, now)
	if err != nil {
		return err
	}

	metrics.RowsRead.Add(int64(len(rows)))

	events := r.collect(rows)

	if len(events) > 0 {
		if err := r.datahub.Push(ctx, events, r.cfg.MaxBatch); err != nil {
			return err
		}
	}

	metrics.EventsPushed.Add(int64(len(events)))
	metrics.Quarantined.Store(int64(r.datahub.Quarantined))
	metrics.SetLastSuccess(now)

	if err := state.Save(r.cfg.StateFile, state.State{LastSuccess: now}); err != nil {
		r.log.Warn("checkpoint save failed (safe, will re-export)", "err", err)
	}

	r.log.Info("cycle complete", "window", from.Format(time.RFC3339)+".."+now.Format(time.RFC3339), "rows", len(rows), "events", len(events), "quarantined", r.datahub.Quarantined)

	return nil
}

func (r *Runner) collect(rows []bifrost.Log) []datahub.Event {
	opts := mapper.Options{
		Dataset:         r.cfg.Dataset,
		FeatureSource:   mapper.FeatureSource(r.cfg.FeatureSource),
		EmitTraceLabels: r.cfg.EmitTraceLabels,
	}

	if r.cfg.Mode == config.ModeDaily {
		return mapper.DailyToEvents(rows, opts)
	}

	events := make([]datahub.Event, 0, len(rows))

	for _, row := range rows {
		event, ok, err := mapper.LogToEvent(row, opts)
		if err != nil {
			r.log.Warn("row skipped", "err", err)
			continue
		}

		if ok {
			events = append(events, event)
		}
	}

	return events
}
