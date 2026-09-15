package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Mode string

const (
	ModePerCall Mode = "per_call"
	ModeDaily   Mode = "daily"
)

type Config struct {
	BifrostBaseURL       string
	BifrostAdminUsername string
	BifrostAdminPassword string
	DoiTAPIURL           string
	DoiTAPIKey           string
	Dataset              string
	DatasetLogoName      string
	Mode                 Mode
	PollInterval         time.Duration
	LookbackHours        int
	BackfillDays         int
	FeatureSource        string
	EmitTraceLabels      bool
	StateFile            string
	MetricsAddr          string
	MaxBatch             int
}

const datasetDescription = "Estimated LLM spend and attribution per gateway request, pushed from the self-hosted Bifrost gateway by the DoiT bifrost-datahub-exporter. Labeled by provider, model, virtual key, team, and customer. Cost basis: Bifrost's model-pricing catalog (showback, not billable actuals — the billed spend arrives on your provider and cloud billing feeds)."

func (c Config) DatasetDescription() string { return datasetDescription }

func FromEnv() (Config, error) {
	c := Config{
		BifrostBaseURL:       strings.TrimRight(getenv("BIFROST_BASE_URL", "http://localhost:8080"), "/"),
		BifrostAdminUsername: os.Getenv("BIFROST_ADMIN_USERNAME"),
		BifrostAdminPassword: os.Getenv("BIFROST_ADMIN_PASSWORD"),
		DoiTAPIURL:           strings.TrimRight(getenv("DOIT_API_URL", "https://api.doit.com"), "/"),
		DoiTAPIKey:           os.Getenv("DOIT_API_KEY"),
		Dataset:              getenv("DATASET", "Bifrost"),
		DatasetLogoName:      getenv("DATASET_LOGO_NAME", "bifrost"),
		Mode:                 Mode(getenv("MODE", string(ModePerCall))),
		FeatureSource:        getenv("FEATURE_SOURCE", "app"),
		EmitTraceLabels:      getenv("EMIT_TRACE_LABELS", "false") == "true",
		StateFile:            getenv("STATE_FILE", "state.json"),
		MetricsAddr:          getenv("METRICS_ADDR", ":9464"),
	}

	var err error
	if c.PollInterval, err = time.ParseDuration(getenv("POLL_INTERVAL", "5m")); err != nil {
		return c, fmt.Errorf("POLL_INTERVAL: %w", err)
	}
	if c.LookbackHours, err = atoi("LOOKBACK_HOURS", "48"); err != nil {
		return c, err
	}
	if c.BackfillDays, err = atoi("BACKFILL_DAYS", "0"); err != nil {
		return c, err
	}
	if c.MaxBatch, err = atoi("MAX_BATCH", "5000"); err != nil {
		return c, err
	}
	if c.MaxBatch < 1 || c.MaxBatch > 50000 {
		return c, fmt.Errorf("MAX_BATCH must be within [1, 50000] (DataHub caps a request at 50000 events)")
	}
	if c.DoiTAPIKey == "" {
		return c, fmt.Errorf("DOIT_API_KEY is required")
	}
	if (c.BifrostAdminUsername == "") != (c.BifrostAdminPassword == "") {
		return c, fmt.Errorf("BIFROST_ADMIN_USERNAME and BIFROST_ADMIN_PASSWORD must be set together")
	}
	if c.Mode != ModePerCall && c.Mode != ModeDaily {
		return c, fmt.Errorf("MODE must be %q or %q", ModePerCall, ModeDaily)
	}
	if c.FeatureSource != "app" && c.FeatureSource != "none" {
		return c, fmt.Errorf("FEATURE_SOURCE must be \"app\" or \"none\"")
	}

	return c, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}

func atoi(key, def string) (int, error) {
	n, err := strconv.Atoi(getenv(key, def))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}

	return n, nil
}
