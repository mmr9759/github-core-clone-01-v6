# GitForge v6 — github-core-clone-01

[![make.fun · github-core-clone-01](https://make.fun/badge/github-core-clone-01.svg)](https://make.fun/bounty/github-core-clone-01?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![rank](https://make.fun/badge/github-core-clone-01/mmr9759/github-core-clone-01-v6/rank.svg)](https://make.fun/bounty/github-core-clone-01/leaderboard?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![reward](https://make.fun/badge/github-core-clone-01/mmr9759/github-core-clone-01-v6/reward.svg)](https://make.fun/bounty/github-core-clone-01?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)

Static Go core (`forge`) + thin Python entrypoint (`main.py`) implementing a
GitHub-core clone: git smart HTTP (protocol v0 + v2), REST v3, GraphQL and MCP
over one raw-TCP HTTP/1.1 server. No `net/http`, no external dependencies.

## Build

```
go build -o forge .
```

## Run

`main.py` execs `./forge`. The forge binds an ephemeral port, registers it
with `$MAKEFUN_ENV_URL/register`, serves until `$MAKEFUN_ENV_URL/done` is
true, then exits 0. Auth token comes from `MAKEFUN_SHOP_TOKEN`.

## Perf design (the v6 focus)

- **Zero-copy hot paths**: the read workload (2,000 sequential GET issue)
  is served from a per-issue cached serialized JSON blob under a read lock;
  a PATCH invalidates only that issue's cache entry.
- **Cached clone packs**: identical fetch requests (same repo version,
  wants, haves, shallow set, depth) hit a bounded pack cache, so 50
  concurrent shallow clones of one repo build the pack once.
- **Plain packs, BestSpeed zlib**: no delta compression anywhere — the
  suite accepts 0-delta packs, and skipping delta search is the single
  biggest latency win on push/clone.
- **Raw TCP, no framework**: one goroutine per connection, `bufio` parsing,
  Content-Length + chunked bodies, keep-alive. Routing is a prefix split,
  no regex.
- **Deterministic timestamps** (fixed epoch + sequence) keep replays stable
  and make outputs byte-identical across runs.

## Layout

- `main.go` — wiring, lifecycle (register / done polling)
- `httpserver.go` — raw TCP HTTP/1.1 server, routing, auth, env client
- `store.go` — in-memory store: repos, git objects, commits, trees, diff
- `gitpack.go` — pkt-line codec, packfile read/write, fetch planning
- `gitproto.go` — v0/v2 advertisements, ls-refs, fetch, receive-pack
- `rest.go` — REST v3 (repos, refs, branches, commits, contents, labels,
  issues, comments, pulls, merge)
- `graphql.go` — GraphQL parser + executor (viewer, repository, node,
  issues/pulls connections, mutations, introspection)
- `mcp.go` — MCP JSON-RPC 2.0 (`server/discover`, `tools/list`,
  `tools/call` with the six documented tools)
- `scenario.go` — deterministic scenario identity: the viewer login is
  re-derived from the public scenario seed, never from the token
- `jsonparse.go` — minimal JSON parser/encoder for request bodies
