#!/usr/bin/env bash
# Integration test: real Bifrost gateway (docker) -> exporter -> local
# DataHub stub, with a mock upstream that Bifrost prices from its own
# catalog. Usage: scripts/integration-test.sh [bifrost image tag, default latest]
set -euo pipefail

TAG="${1:-latest}"
DIR="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'docker rm -f bifrost-itest >/dev/null 2>&1 || true; [ -n "${STUB_PID:-}" ] && kill "$STUB_PID" 2>/dev/null || true; [ -n "${MOCK_PID:-}" ] && kill "$MOCK_PID" 2>/dev/null || true; rm -rf "$WORK"' EXIT

go build -o "$WORK/mock-provider" "$DIR/scripts/mock-provider"
"$WORK/mock-provider" :18742 &
MOCK_PID=$!

go build -o "$WORK/datahub-stub" "$DIR/scripts/datahub-stub"
"$WORK/datahub-stub" :8181 &
STUB_PID=$!
sleep 1

mkdir -p "$WORK/data"
chmod 777 "$WORK/data"
cat > "$WORK/data/config.json" <<'EOF'
{
  "$schema": "https://www.getbifrost.ai/schema",
  "client": { "enable_logging": true },
  "config_store": { "enabled": true, "type": "sqlite", "config": { "path": "/app/data/config.db" } },
  "logs_store": { "enabled": true, "type": "sqlite", "config": { "path": "/app/data/logs.db" } },
  "providers": {
    "openai": {
      "keys": [{ "id": "key-mock-openai", "name": "mock-openai", "value": "mock-key", "weight": 1, "models": ["*"] }],
      "network_config": { "base_url": "http://host.docker.internal:18742", "allow_private_network": true, "max_retries": 1 }
    },
    "anthropic": {
      "keys": [{ "id": "key-mock-anthropic", "name": "mock-anthropic", "value": "mock-key", "weight": 1, "models": ["*"] }],
      "network_config": { "base_url": "http://host.docker.internal:18742", "allow_private_network": true, "max_retries": 1 }
    }
  },
  "governance": {
    "customers": [{ "id": "cust-acme", "name": "acme-corp", "budget_id": "b-cust" }],
    "teams": [{ "id": "team-growth", "name": "growth", "customer_id": "cust-acme" }],
    "virtual_keys": [
      { "id": "vk-growth", "name": "itest-vk", "value": "sk-bf-itest", "is_active": true, "team_id": "team-growth",
        "provider_configs": [
          { "provider": "openai", "key_ids": ["key-mock-openai"], "allowed_models": ["*"], "weight": 1 },
          { "provider": "anthropic", "key_ids": ["key-mock-anthropic"], "allowed_models": ["*"], "weight": 1 }
        ] }
    ],
    "budgets": [{ "id": "b-cust", "max_limit": 5000, "reset_duration": "1M" }]
  }
}
EOF

docker run -d --name bifrost-itest -p 18743:8080 \
  --add-host=host.docker.internal:host-gateway \
  -v "$WORK/data:/app/data" "maximhq/bifrost:$TAG" >/dev/null

READY=0
for _ in $(seq 1 60); do
  if curl -sf -o /dev/null http://localhost:18743/api/logs; then READY=1; break; fi
  sleep 3
done
if [ "$READY" != 1 ]; then
  echo "FAIL: gateway never became ready; container logs follow" >&2
  docker logs bifrost-itest >&2 || true
  exit 1
fi

for i in 1 2 3; do
  curl -sf -o /dev/null -X POST http://localhost:18743/v1/chat/completions \
    -H 'Content-Type: application/json' -H 'x-bf-vk: sk-bf-itest' \
    -d "{\"model\":\"openai/gpt-4o-mini\",\"messages\":[{\"role\":\"user\",\"content\":\"itest req $i CANARY-a7f3e9\"}]}"
done
curl -sf -o /dev/null -X POST http://localhost:18743/v1/chat/completions \
  -H 'Content-Type: application/json' -H 'x-bf-vk: sk-bf-itest' \
  -d '{"model":"anthropic/claude-sonnet-4-20250514","messages":[{"role":"user","content":"itest anthropic CANARY-a7f3e9"}]}'
curl -s -o /dev/null -X POST http://localhost:18743/v1/chat/completions \
  -H 'Content-Type: application/json' -H 'x-bf-vk: sk-bf-itest' \
  -d '{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"please fail"}]}' || true

# Log rows are written asynchronously; wait until the finalized rows land.
for _ in $(seq 1 20); do
  COUNT=$(curl -sf 'http://localhost:18743/api/logs?limit=1' | sed -n 's/.*"total_count":\([0-9]*\).*/\1/p')
  [ "${COUNT:-0}" -ge 5 ] && break
  sleep 3
done

run_once() {
  BIFROST_BASE_URL=http://localhost:18743 \
  DOIT_API_URL=http://localhost:8181 DOIT_API_KEY=stub DATASET=Bifrost \
  STATE_FILE="$WORK/state-$1.json" MODE="$1" go run "$DIR/cmd/exporter" --once
}

run_once per_call
run_once per_call  # idempotency: re-run must not error and must not add unique events

RECEIVED=$(curl -sf http://localhost:8181/received | sed 's/[^0-9]//g')
if [ "$RECEIVED" -lt 4 ]; then
  echo "FAIL: expected >=4 unique per-call events at the stub (3 openai + 1 anthropic), got $RECEIVED" >&2
  exit 1
fi

if curl -sf http://localhost:8181/received-full 2>/dev/null | grep -q 'CANARY-a7f3e9'; then
  echo "FAIL: prompt content leaked into exported events" >&2
  exit 1
fi

run_once daily

RECEIVED_AFTER_DAILY=$(curl -sf http://localhost:8181/received | sed 's/[^0-9]//g')
if [ "$RECEIVED_AFTER_DAILY" -le "$RECEIVED" ]; then
  echo "FAIL: daily mode added no events ($RECEIVED -> $RECEIVED_AFTER_DAILY)" >&2
  exit 1
fi

echo "PASS: $RECEIVED per-call + $((RECEIVED_AFTER_DAILY - RECEIVED)) daily unique events exported against bifrost:$TAG"
