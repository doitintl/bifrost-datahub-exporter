# Bifrost Exporter

Export estimated LLM spend and attribution from a self-hosted [Bifrost](https://github.com/maximhq/bifrost) gateway into [DoiT Cloud Intelligence](https://www.doit.com/cloud-intelligence/) via the [DataHub API](https://help.doit.com/docs/datahub).

One small container/binary runs next to your gateway, polls its documented logs API (`GET /api/logs`) incrementally, and pushes one labeled DataHub event per gateway request — provider, model, virtual key, team, customer, plus DoiT's `genai/*` GenAI taxonomy. Your reports, allocations, budgets, and anomaly detection see per-request LLM attribution alongside your cloud bill.

**What the numbers mean.** Bifrost bills nothing — you bring your own provider keys, and the authoritative spend arrives on your provider and cloud billing feeds. Bifrost prices each request from its model-pricing catalog, and this exporter ships those numbers as **estimates for attribution/showback** (`cost_basis: estimated` on every event). Do not add this dataset to your cloud bill; use it to answer *who and what* drove the spend. Bifrost can also restate historical costs after pricing changes (`recalculate-cost`); the exporter's lookback re-poll absorbs recent restatements, and a `--backfill` re-run repairs deeper ones — re-exports overwrite by event id, never duplicate.

Design and validation record: `specs/CloudIntelligence/bifrost-datahub-spend-integration/` in DoiT's engineering monorepo. Sibling project: [litellm-datahub-exporter](https://github.com/doitintl/litellm-datahub-exporter).

## Quick start

```bash
docker run --rm \
  -e BIFROST_BASE_URL=http://your-bifrost:8080 \
  -e DOIT_API_KEY=$DOIT_API_KEY \
  -v exporter-state:/state \
  ghcr.io/doitintl/bifrost-datahub-exporter:latest
```

- `DOIT_API_KEY`: a DoiT console API token with the **DataHubAdmin** permission (User view → API; requires a DataHub subscription).
- The gateway needs no credentials by default; if you enabled governance admin auth (`governance.auth_config`), also set `BIFROST_ADMIN_USERNAME`/`BIFROST_ADMIN_PASSWORD`.
- Within ~15 minutes the `Bifrost` dataset appears in the console (DataHub → Datasets) and in Cloud Analytics as the `Bifrost` cloud provider.

Run `--once` for a single cycle (cron-friendly), `BACKFILL_DAYS=N` for history (bounded by your logstore retention and DataHub's ±2-year window).

## Deployment options

Supported Bifrost versions: **transports v2.0.0 and later** (the release line with the input/output cost split); CI tests every release against v2.0.0, v2.1.x, and `latest`. Older v1.6.x gateways work best-effort — the startup probe logs what it found and the Input/Output Cost metrics are absent.

Place **one exporter per Bifrost deployment**, not per replica: gateway replicas share one logstore, so a single poller against the service address covers them all. The exporter sits outside the LLM request path, accepts no inbound traffic (optional health/metrics listener aside), and needs exactly two network paths: the gateway and `api.doit.com:443`.

### Kubernetes (Helm)

```bash
helm install bifrost-exporter ./charts/bifrost-datahub-exporter \
  --set bifrostBaseUrl=http://bifrost.llm.svc:8080 \
  --set doitApiKey=$DOIT_API_KEY
```

Single-replica hardened Deployment (scratch image, read-only rootfs, no capabilities), optional PVC for the checkpoint, optional egress NetworkPolicy — see [values.yaml](charts/bifrost-datahub-exporter/values.yaml).

### docker-compose

See [docker-compose.example.yaml](docker-compose.example.yaml).

### systemd (VM / bare metal)

See [deploy/bifrost-datahub-exporter.service](deploy/bifrost-datahub-exporter.service); release binaries for linux/darwin × amd64/arm64 are on the releases page.

### Air-gapped / strict egress

The exporter is the single egress point to allowlist: outbound HTTPS to `api.doit.com` only, plus the in-network gateway. No telemetry, no phone-home — verify in a few hundred lines of client code.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `BIFROST_BASE_URL` | `http://localhost:8080` | Gateway URL (cluster-internal is fine) |
| `BIFROST_ADMIN_USERNAME` / `BIFROST_ADMIN_PASSWORD` | — | Only when governance admin auth is enabled on the gateway |
| `DOIT_API_KEY` | required | DoiT API token with the DataHubAdmin scope |
| `DOIT_API_URL` | `https://api.doit.com` | DoiT API host (`https://api-dev.doit.com` for DoiT-internal testing — tokens are per-host) |
| `DATASET` | `Bifrost` | DataHub dataset name; use an `env` label for multi-gateway estates, a separate dataset only for hard separation |
| `MODE` | `per_call` | `per_call` (one event per request) or `daily` (client-side day × provider × model × key × team × customer aggregate for very high volume) |
| `FEATURE_SOURCE` | `app` | Map Bifrost's app attribution to the `feature` label (`none` disables). Bifrost buckets unknown user agents as "Other" (never exported); register your apps under the gateway's user-agent mappings to get meaningful values |
| `POLL_INTERVAL` | `5m` | Cycle interval |
| `LOOKBACK_HOURS` | `48` | Trailing re-read window — covers async log writes, late finalization, and near-term cost restatements |
| `BACKFILL_DAYS` | `0` | On first run (no checkpoint), export this much history |
| `EMIT_TRACE_LABELS` | `false` | Also label `request_id`, `parent_trace_id` (fallback-chain root), `session_id` — high-cardinality, off by default |
| `MAX_BATCH` | `5000` | Events per DataHub request (hard cap 50000) |
| `STATE_FILE` | `state.json` (`/state/state.json` in the container) | Checkpoint; losing it is safe (idempotent re-export) |
| `METRICS_ADDR` | `:9464` | Prometheus `/metrics` + `/healthz` listener, empty to disable |
| `DATASET_LOGO_NAME` | `bifrost` | Preset dataset icon (ignored by DoiT API versions without it) |

## What gets exported

Per finalized gateway request (`success` and `error` rows; in-flight rows are picked up once they settle):

- **Dimensions**: provider, model, virtual key, team, customer, business unit and project (when your plan populates them), `feature` (from app attribution), and `genai/*` system labels (model family, PAYG, API key name).
- **Metrics**: `cost` (the catalog estimate; `0` on error rows), `usage` (total tokens), Prompt/Completion Tokens, and Input/Output/Additional Cost on gateways ≥ v2.0.0.
- Fallback chains export one event per attempt, tagged `bifrost/fallback_index`, with the chain root available as `parent_trace_id` behind `EMIT_TRACE_LABELS`.

**Privacy**: the exporter decodes log rows through a strict field allowlist — prompt and completion content (`input_history`, `content_summary`, raw bodies) has no code path to DoiT, enforced by a leak-canary test in CI. The gateway credentials never leave your network; the DoiT token is scoped to DataHub only.

## Verifying a release

```bash
# container image
cosign verify ghcr.io/doitintl/bifrost-datahub-exporter:<tag> \
  --certificate-identity-regexp 'https://github.com/doitintl/bifrost-datahub-exporter/.github/workflows/release.yml@.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

# binary checksums
cosign verify-blob --bundle checksums.txt.sigstore.json checksums.txt \
  --certificate-identity-regexp 'https://github.com/doitintl/bifrost-datahub-exporter/.github/workflows/release.yml@.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum -c checksums.txt
```

Builds are reproducible (`CGO_ENABLED=0`, `-trimpath`, pinned toolchain); images ship BuildKit provenance and SBOM attestations.

## Building from source

```bash
make build    # bin/bifrost-datahub-exporter
make test
./scripts/integration-test.sh v2.1.1   # real gateway in docker + mock upstream + DataHub stub
```

## License

Apache-2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).
