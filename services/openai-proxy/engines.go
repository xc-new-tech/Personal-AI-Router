// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"os"
	"strings"

	"nvpair-shared/noderec"
)

// openaiEngines is the set of engine-manager engines this facade fronts, in
// preference order when more than one on a node serves the same model name.
//
// One facade serves all three rather than one facade each: they speak the same
// OpenAI API, so a client has no reason to care which of them answers, and a
// single ingress lets a request for a model be routed to whichever node and
// engine actually holds it. That also keeps this to one advertised service key
// (noderec.ServiceOpenAI) in a size-limited TXT record.
//
// Preference order matters only for the tie — a node running two of these with
// the same model name loaded. oMLX leads because it is the one with a
// persistent KV cache, so repeat traffic for a model it already holds is the
// cheapest place to land.
var openaiEngines = []string{"omlx", "vllm", "sglang"}

// engineHeader lets a forwarding proxy name the engine it selected, so the
// receiving node's cluster ingress forwards to the same engine the ranking
// decision was made against. Without it the ingress would have to re-parse the
// request body to recover the model, and a node running more than one of these
// engines could answer from the wrong one.
const engineHeader = "X-NVPAIR-Engine"

// attributionFor copies a directory node's per-engine model lists, keeping only
// the engines this facade fronts. Nil when the node carries no attribution at
// all, which is the signal callers use to fall back instead of guessing.
func attributionFor(n noderec.DirectoryNode, engines []string) map[string][]string {
	if n.ModelsByEngine == nil {
		return nil
	}
	out := make(map[string][]string, len(engines))
	for _, engine := range engines {
		if models := n.ModelsByEngine[engine]; len(models) > 0 {
			out[engine] = append([]string(nil), models...)
		}
	}
	return out
}

// engineForModel returns which of this facade's engines serves model on the
// given node, using the node's per-engine attribution. It returns "" when the
// node carries no attribution (a pre-attribution or mixed-version peer) or when
// none of our engines claims the model — callers treat that as "unknown" and
// fall back rather than guessing an engine.
func engineForModel(n Node, model string) string {
	if model == "" || n.ModelsByEngine == nil {
		return ""
	}
	for _, engine := range openaiEngines {
		for _, m := range n.ModelsByEngine[engine] {
			if m == model {
				return engine
			}
		}
	}
	return ""
}

// engineAPIKeyEnv is the environment variable an engine's credential is read
// from, matching nvpair-engine-manager's convention exactly:
// NVPAIR_<ENGINE>_API_KEY, upper-cased, non-alphanumerics folded to "_".
func engineAPIKeyEnv(engine string) string {
	var b strings.Builder
	b.WriteString("NVPAIR_")
	for _, r := range strings.ToUpper(engine) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	b.WriteString("_API_KEY")
	return b.String()
}

// authorizeLocal attaches this node's credential for engine to a request being
// forwarded to a *local* backend.
//
// Only local: a peer authenticates with its own engine's credential, so sending
// ours to a peer would leak it and still fail there. The proxy therefore adds it
// on exactly the two hops that terminate at this node's own engine.
//
// An Authorization the caller already set is left alone, so a client that
// manages the engine credential itself keeps working.
//
// The credential is read from the environment, never from disk or config: it is
// the same secret nvpair-engine-manager probes with, and keeping it to one
// injection point makes the facade's loopback-only plaintext gate the thing that
// protects it.
func authorizeLocal(r *http.Request, engine string) {
	if engine == "" || r.Header.Get("Authorization") != "" {
		return
	}
	if key := strings.TrimSpace(os.Getenv(engineAPIKeyEnv(engine))); key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
}

// engineFromRequest reads the engine a forwarding proxy selected, returning ""
// when the header is absent or names an engine this facade does not front.
func engineFromRequest(r *http.Request) string {
	got := r.Header.Get(engineHeader)
	for _, engine := range openaiEngines {
		if got == engine {
			return engine
		}
	}
	return ""
}
