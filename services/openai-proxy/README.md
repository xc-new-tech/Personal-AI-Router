<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# OpenAI Proxy

A discovery-aware HTTP reverse proxy fronting every engine on the network that
speaks the OpenAI API. It runs no mDNS browse of its own: its routing targets
come from the broker's discovery relay (it sends
`discovery:subscribe {services:[oa]}` and replaces its routing overlay from each
pushed `discovery:nodes` snapshot) plus user-added manual nodes. It forwards HTTP
requests to the selected node, aggregates the model-list route across candidate
nodes, and exposes a bidirectional JSON-RPC 2.0 control channel over stdio (or an
IPC socket).

> **Clone of `ollama-proxy`.** Routing, failover, CORS, node selection, workload
> reporting and the control channel are identical to
> [`ollama-proxy`](../ollama-proxy/README.md) — the CORS policy is literally the
> same code, `nvpair-shared/cors`, and is documented
> [there](../ollama-proxy/README.md#http-reverse-proxy). Read that README for the
> shared behavior; only the differences below are specific to this proxy.

## What is different

**It fronts a family of engines, not one.** oMLX, vLLM and SGLang all speak the
same API, so a client has no reason to care which of them answers. One facade
lets a request for a model be routed to whichever node *and engine* actually
holds it, and keeps clients on a single endpoint. `ollama-proxy` and
`lmstudio-proxy` each front exactly one engine; this one fronts `openaiEngines`
(see `engines.go`), in that preference order — oMLX leads because it is the one
with a persistent KV cache, so repeat traffic for a model it already holds is
the cheapest place to land.

That single difference produces the rest:

| | `ollama-proxy` / `lmstudio-proxy` | `openai-proxy` |
| --- | --- | --- |
| Service key | `ol` / `lm` | `oa` — one key, not one per engine |
| Local backend | one | one **per engine** (`backends` map) |
| Routing candidate | node | node **+ engine** |
| Workload engine | fixed constant | the selected engine, or `openai` when unknown |
| Default port | 11434 / 1234 | 11436 |

**One service key, not three.** Like `ol` and `lm`, `oa` advertises the
*proxy's* port, not the engine's: a peer reaches this node's engines through this
node's proxy, which then picks the right local backend. Keeping it to one key
also matters because these are emitted into a size-limited mDNS TXT record.

**Per-engine local backends.** A node can run oMLX on 8123 and vLLM on 8000 at
once. The `node/set-local-backend` wire message already carries an `engine`
field, so the broker sends one per engine and this proxy keeps them in a map
keyed by engine.

**Engine selection travels with the request.** The selected engine is sent as
`X-NVPAIR-Engine`, so the receiving node's cluster ingress serves from the same
engine the ranking decision was made against instead of re-parsing the request
body to recover the model. The header is unconditionally deleted and re-set in
the `Director`, so a client cannot use it to steer which engine a peer serves
from.

**Ambiguity is refused, never guessed.** When per-engine attribution cannot name
an engine — a peer that sends none — the proxy forwards only if exactly one local
backend is healthy, and answers 503 otherwise. Answering from the wrong engine
would serve a different model than the caller ranked, which is worse than
failing. For the same reason the workload's `engine` is reported as `openai`
rather than inventing a specific engine name, so a job card is never mislabeled.

## Build

```bash
go build -o openai-proxy .
```

## Usage

Flags match `lmstudio-proxy`: `--port`, `--ignore-persisted-port`, `--ipc`,
`--cluster-dir`, `--version`, `--log-level`. The persisted port lives in its own
file, `openai-proxy-port.json`. Like `lmstudio-proxy` it has no
`--alias-address`, so its self-forward guard covers only its own listener.
