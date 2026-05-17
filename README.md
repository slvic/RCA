# rca-agent

Automated root cause analysis for production alerts. When Alertmanager fires a critical alert, rca-agent queries your observability stack (Prometheus, Loki, Tempo, Grafana), preprocesses the raw telemetry into signals, and synthesizes a structured hypothesis using Claude.

Not a chatbot. Not a LangChain wrapper. A focused Go service.

---

## How it works

```
Alertmanager webhook
        │
        ▼
  Dedup filter ──► (already seen → drop)
        │
        ▼
  Smart MCP tools (parallel)
  ├── detect_anomalies   → z-score vs 7-day baseline
  ├── get_error_summary  → clustered error patterns
  ├── correlate_events   → deployments, restarts, config changes
  └── get_slow_traces    → critical path of slowest spans
        │
        ▼
  Single LLM call (Claude)
        │
        ▼
  RCAReport → audit log + structured log output
```

Two investigation modes:

| Mode | When to use | How it works |
|---|---|---|
| `plan` (default) | Webhook alerts, speed matters | All tools in parallel → one LLM synthesis call |
| `react` | Exploratory, unknown alerts, postmortems | LLM iteratively picks tools and reasons step-by-step |

---

## Prerequisites

- Go 1.23+
- An Anthropic API key (`claude-sonnet-4-20250514` or later)
- MCP servers already running for your o11y stack (Prometheus, Loki, Tempo, Grafana)

---

## Installation

```bash
git clone https://github.com/slvic/rca-agent
cd rca-agent
go build -o rca-agent ./cmd/agent
```

---

## Configuration

Copy the example config and fill in your values:

```bash
cp config.yaml.example config.yaml
```

```yaml
# config.yaml
llm:
  model: "claude-sonnet-4-20250514"
  # api_key: set via ANTHROPIC_API_KEY env var instead

mcp_servers:
  prometheus:
    url: "http://prometheus-mcp:8080"
    timeout: 30s
  loki:
    url: "http://loki-mcp:8080"
    timeout: 30s
  tempo:
    url: "http://tempo-mcp:8080"
    timeout: 30s
  grafana:
    url: "http://grafana-mcp:8080"
    timeout: 30s

webhook:
  port: 8080
  dedup_ttl: 10m                    # suppress duplicate alerts within window
  allowed_severities: [critical, page]

investigation:
  timeout: 5m
  mode: "plan"                      # "plan" | "react"
  react_max_iter: 8                 # max LLM iterations in react mode

output:
  # slack_webhook_url: set via SLACK_WEBHOOK_URL env var
```

**Required env vars:**

```bash
export ANTHROPIC_API_KEY=sk-ant-...
export SLACK_WEBHOOK_URL=https://hooks.slack.com/...   # optional for now
```

---

## Running

### Server mode (receives Alertmanager webhooks)

```bash
./rca-agent --config config.yaml
```

The agent listens on `:8080` and exposes:
- `POST /webhook/alert` — Alertmanager webhook receiver
- `GET  /healthz`        — liveness check

### CLI mode (single alert, for testing)

```bash
./rca-agent --config config.yaml --alert-file testdata/checkout_alert.json
```

Prints the full `RCAReport` as JSON and a markdown summary to stdout. Also writes an audit log to `./audits/`.

#### Force react mode from CLI

```bash
./rca-agent --config config.yaml --alert-file testdata/checkout_alert.json --mode react
```

---

## Alertmanager integration

Add to your `alertmanager.yaml`:

```yaml
receivers:
  - name: rca-agent
    webhook_configs:
      - url: 'http://rca-agent.monitoring.svc.cluster.local:8080/webhook/alert'
        send_resolved: false

route:
  routes:
    - match:
        severity: critical
      receiver: rca-agent
      continue: true    # still sends to PagerDuty/Slack in parallel
```

To trigger react mode for specific alerts, add `?mode=react` to the URL:

```yaml
url: 'http://rca-agent.monitoring.svc.cluster.local:8080/webhook/alert?mode=react'
```

---

## Output

The agent currently logs the report as structured JSON (Slack output is post-MVP). Every investigation writes an audit file regardless of success or failure.

**Audit log location:** `./audits/YYYYMMDD-HHMMSS-ALERTNAME.json`

**Log output (structured JSON):**
```json
{
  "level": "INFO",
  "msg": "rca_report",
  "alert": "CheckoutHighErrorRate",
  "root_cause": "PostgreSQL connection pool exhausted — connections leaked after v2.4.2 deploy",
  "confidence": 0.87
}
```

**RCAReport shape:**
```json
{
  "alert_id": "checkout-error-rate-001",
  "investigated_at": "2024-01-15T14:03:15Z",
  "duration_ms": 8240,
  "root_cause": "PostgreSQL connection pool exhausted...",
  "confidence": 0.87,
  "evidence": [
    { "description": "error rate 4.5% vs 0.1% baseline", "source": "prometheus", "metric": "error_rate" }
  ],
  "timeline": [
    { "time": "2024-01-15T14:01:23Z", "description": "Deployment checkout v2.4.2", "source": "deployment" }
  ],
  "findings": [...],
  "next_steps": ["Check pg_stat_activity for idle connections"],
  "react_steps": [...],   // only present in react mode
  "llm_tokens_used": 3140
}
```

---

## Investigation modes in detail

### Plan mode (default)

All four tools run in parallel with a 45-second combined ceiling. Results are bundled into a single LLM prompt. Faster and cheaper — the right choice for automated webhook handling.

```
tools (parallel, 45s max)  →  one LLM call (60s max)  →  RCAReport
```

### React mode

The LLM decides which tool to call next based on what it has already seen, reasoning step-by-step.

```
iter 1: LLM → "call detect_anomalies"  →  tool result
iter 2: LLM → "call get_error_summary" →  tool result
iter 3: LLM → "final_answer"           →  RCAReport
```

Each step is recorded in `react_steps`, giving you a full reasoning trace in the audit log. Useful for:
- Exploratory investigation of unfamiliar alert types
- Postmortem analysis where depth matters more than speed
- Alerts without a clear owning service

**Cost note:** React mode uses up to `react_max_iter` LLM calls (default 8). Each iteration includes the growing history in the prompt. For standard webhook alerting, stick with `plan`.

---

## Kubernetes deployment

```bash
kubectl apply -f deploy/k8s/configmap.yaml
kubectl apply -f deploy/k8s/service.yaml
kubectl apply -f deploy/k8s/deployment.yaml
```

Create the secrets first:

```bash
kubectl create secret generic rca-agent-secrets \
  --namespace monitoring \
  --from-literal=anthropic-api-key=$ANTHROPIC_API_KEY \
  --from-literal=slack-webhook-url=$SLACK_WEBHOOK_URL
```

The deployment runs a single replica (intentional — deduplication is in-memory).

---

## Docker

```bash
docker build -f deploy/docker/Dockerfile -t rca-agent:latest .

docker run \
  -e ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY \
  -v $(pwd)/config.yaml:/etc/rca-agent/config.yaml \
  -p 8080:8080 \
  rca-agent:latest --config /etc/rca-agent/config.yaml
```

---

## Development

```bash
# Run tests
go test ./...

# Test a specific alert locally (no MCP servers needed for unit tests)
go test ./internal/agent/... -v -run TestParseLLMOutput

# Build and smoke-test CLI mode
go build -o rca-agent ./cmd/agent
./rca-agent --config config.yaml --alert-file testdata/checkout_alert.json
```

**Testdata alerts:**
- `testdata/checkout_alert.json` — high error rate on checkout service
- `testdata/db_latency_alert.json` — PostgreSQL p99 latency spike
- `testdata/oom_alert.json` — container OOMKilled repeatedly

**Audit logs** in `./audits/` are your training data. Keep them — they're how you improve prompts and catch regressions.

---

## Project layout

```
cmd/agent/          entrypoint, flag parsing, server + CLI modes
internal/agent/     investigation orchestration (plan + react)
internal/mcp/       MCP client + smart tools (z-score, log clustering, trace ranking)
internal/llm/       Anthropic HTTP client
internal/webhook/   Alertmanager receiver + in-memory dedup
internal/report/    shared types + markdown/JSON formatters
internal/config/    YAML config loading with env overrides
deploy/             Dockerfile + Kubernetes manifests
testdata/           Sample alert JSON files
```

---

## What's not in MVP

- Slack output (reports are logged as JSON; wire `output.slack_webhook_url` when ready)
- `compare_profiles` (Pyroscope tool is stubbed — returns "not available in MVP")
- Webhook auth (add HMAC verification before exposing to untrusted networks)
- Redis dedup (in-memory is fine for a single replica)
