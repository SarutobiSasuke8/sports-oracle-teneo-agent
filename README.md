# Sports Oracle — Teneo Protocol Agent

A [Teneo Protocol](https://teneo.pro) agent that wraps the **Sports Oracle API**, exposing real-time sports data (injuries, live scores, schedules, standings, teams) across 8 sports as paid on-chain agent commands priced at **$0.001 per command**.

**This service contains no payment code.** Payment enforcement (x402 USDC micropayments) is expected from the Teneo runtime that fronts the agent. Only expose this service through that runtime — never directly to the public internet — or every command it serves is served for free.

- REST upstream: `{SPORTS_ORACLE_BASE_URL}/api/v1/{sport}/{resource}` (79 endpoints)
- MCP upstream: `{SPORTS_ORACLE_BASE_URL}/api/mcp` (18 tools)
- Auth: `X-Oracle-Key` header (injected by this agent; configure via env var)

## Status

**Prototype — the default upstream URL does not currently serve the API.** `https://sports-oracle.vercel.app` returns an SPA HTML shell, and `POST /api/mcp` answers 405, so the agent has no live upstream out of the box. Deploy a Sports Oracle instance (or obtain access to a live one) and point `SPORTS_ORACLE_BASE_URL` at it. The `/health` endpoint reports what it found via the `upstream` field.

### Security note: previously committed sandbox key

Earlier revisions of this repository committed a literal sandbox API key in both `main.go` and this README. That key remains in the git history and **must be considered burned**: revoke/rotate it on the Sports Oracle side and do not reuse it. No key ships with the code any more — the agent refuses to start without `SPORTS_ORACLE_KEY` set.

## Commands

| Command | Sport | Resources |
|---------|-------|-----------|
| `nba` | NBA | injuries, scores, schedule, standings, teams |
| `nhl` | NHL | injuries, scores, schedule, standings, teams |
| `mlb` | MLB | injuries, scores, schedule, standings, teams |
| `nfl` | NFL | injuries, scores, schedule, standings, teams |
| `f1` | Formula 1 | injuries, scores, schedule, standings, teams |
| `soccer` | Soccer | injuries, scores, schedule, standings, teams |
| `tennis` | Tennis | injuries, scores, schedule, standings, teams |
| `mma` | MMA | injuries, scores, schedule, standings, teams |

Every sport supports the same five resources: `injuries`, `scores`, `schedule`, `standings`, `teams`.

## Setup

Requires Go 1.24+.

```bash
git clone https://github.com/SarutobiSasuke8/sports-oracle-teneo-agent
cd sports-oracle-teneo-agent
go build -o sports-oracle-teneo-agent .
```

### Configuration

| Env var | Default | Description |
|---------|---------|-------------|
| `SPORTS_ORACLE_KEY` | **required, no default** | Sports Oracle API key sent as `X-Oracle-Key`. The agent exits at startup with a clear error when it is unset. Use your own sandbox key (e.g. `sk_test_YOUR_KEY`) for testing — live data requires a staked key. |
| `PORT` | `8080` | Port the agent service listens on. |
| `SPORTS_ORACLE_BASE_URL` | `https://sports-oracle.vercel.app` | Upstream base URL. See [Status](#status): the default does not currently serve the API, so point this at a live Sports Oracle instance. |

```bash
export SPORTS_ORACLE_KEY=sk_live_your_staked_key
./sports-oracle-teneo-agent
```

## Usage

### Health check

```bash
curl http://localhost:8080/health
```

```json
{
  "status": "ok",
  "agent": "sports-oracle",
  "version": "1.0.0",
  "upstream": "ok"
}
```

`upstream` is a live connectivity probe of the configured `SPORTS_ORACLE_BASE_URL`: `"ok"` means the upstream answered with JSON (the API layer is there, even if the answer is a JSON error), `"unreachable"` means it did not respond or returned non-JSON (e.g. the SPA HTML shell). The probe never fails the health response itself.

### Execute a command

`POST /command` with the Teneo command payload. `params.resource` is required; any additional params (`team`, `date`, `league`, …) are forwarded to the upstream API as query parameters.

```bash
curl -s http://localhost:8080/command \
  -H 'Content-Type: application/json' \
  -d '{"command": "nba", "params": {"resource": "injuries"}}'
```

Response:

```json
{
  "success": true,
  "command": "nba",
  "resource": "injuries",
  "data": { "...": "upstream Sports Oracle payload" }
}
```

Errors are structured and never leak stack traces:

```json
{
  "success": false,
  "error": {
    "code": "unknown_resource",
    "message": "unsupported resource \"foo\"; supported: injuries, schedule, scores, standings, teams"
  }
}
```

### MCP transport

The agent proxies MCP (JSON-RPC over HTTP) at `/mcp`, injecting the oracle key, so MCP clients can talk to Sports Oracle's 18 tools through the agent:

```bash
curl -s http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}'
```

## Deploying on Teneo

Teneo agents are minted as NFTs, run as background services, and are queryable via the Teneo CLI or MCP.

```bash
# 1. Scaffold the agent on-chain
teneo agent create

# 2. Edit metadata (sports-oracle-agent-metadata.json is the source of truth:
#    agent_type "mcp", 8 sport commands at $0.001 each)

# 3. Deploy the service (build the binary and point the deployment at it,
#    with SPORTS_ORACLE_KEY set in the environment)
teneo agent deploy

# 4. Publish to make the agent publicly queryable
teneo agent publish
```

Once published, anyone can query it:

```bash
teneo query sports-oracle nba --resource injuries
```

## Project layout

```
sports-oracle-agent-metadata.json   Teneo agent metadata (commands, pricing)
main.go                             Agent HTTP service: /command, /mcp proxy, /health
main_test.go                        Tests for command routing and upstream handling
go.mod                              Go module (stdlib only, no dependencies)
```

## License

MIT
