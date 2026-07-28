# Sports Oracle — Teneo Protocol Agent

A [Teneo Protocol](https://teneo.pro) agent that wraps the **Sports Oracle API**, exposing real-time sports data (injuries, live scores, schedules, standings, teams) across 10 sports as paid on-chain agent commands. Payments are handled by x402 USDC micropayments at **$0.001 per command**.

- REST upstream: `https://sports-oracle.vercel.app/api/v1/{sport}/{resource}` (79 endpoints)
- MCP upstream: `https://sports-oracle.vercel.app/api/mcp` (18 tools)
- Auth: `X-Oracle-Key` header (handled by this agent; configure via env var)

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

The service also accepts `wnba` and `esports`, which the upstream API covers. Every command costs $0.001 USDC via x402.

## Setup

Requires Go 1.22+.

```bash
git clone https://github.com/SarutobiSasuke8/sports-oracle-teneo-agent
cd sports-oracle-teneo-agent
go build -o sports-oracle-teneo-agent .
```

### Configuration

| Env var | Default | Description |
|---------|---------|-------------|
| `SPORTS_ORACLE_KEY` | sandbox key | Sports Oracle API key sent as `X-Oracle-Key`. The bundled default (`sk_test_886492645fd15c41a37c4101c8b616a2`) is a **sandbox key for testing only** — live data requires a staked key. |
| `PORT` | `8080` | Port the agent service listens on. |
| `SPORTS_ORACLE_BASE_URL` | `https://sports-oracle.vercel.app` | Upstream base URL (override for testing). |

```bash
export SPORTS_ORACLE_KEY=sk_live_your_staked_key
./sports-oracle-teneo-agent
```

## Usage

### Health check

```bash
curl http://localhost:8080/health
```

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

# 2. Edit metadata (this repo's metadata.json is the source of truth:
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
metadata.json   Teneo agent metadata (commands, pricing, env, MCP endpoint)
main.go         Agent HTTP service: /command, /mcp proxy, /health
go.mod          Go module (stdlib only, no dependencies)
```

## License

MIT
