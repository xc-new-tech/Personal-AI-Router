// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"time"

	"nvpair-shared/applog"
	"nvpair-shared/noderec"
)

// openaiproxy.go is the broker's wiring for openai-proxy, the third engine
// facade. It speaks the same JSON-RPC control plane as ollama-proxy and
// lmstudio-proxy, so it reuses proxyProcess; only the relay namespace differs
// (openai-proxy:).
//
// It is markedly simpler than lmstudioproxy.go because it owns no port
// negotiation. lmstudio-proxy has to take over LM Studio's own well-known
// :1234 and move the real engine out of the way, which is what lmstudioport.go
// exists for. openai-proxy impersonates nobody: its port is PAIR's own, and the
// engines behind it are externally managed, so PAIR never moves their ports.
//
// That also decides what a bind failure means here. The other two facades fall
// back to another port because the port they want belongs to an engine that may
// legitimately be holding it. This facade's port is the address a user's
// clients are configured against, so quietly moving it would break them
// silently. A bind failure is reported and left to the supervisor instead.

// openaiEngines mirrors the engine set openai-proxy fronts. The broker needs it
// to push one local backend per engine; openai-proxy owns the routing-side copy.
var openaiEngines = []string{"omlx", "vllm", "sglang"}

func (b *Broker) setOpenAIProxy(p *proxyProcess) {
	b.workersMu.Lock()
	b.openaiProxy = p
	b.workersMu.Unlock()
}

func (b *Broker) getOpenAIProxy() *proxyProcess {
	b.workersMu.Lock()
	defer b.workersMu.Unlock()
	return b.openaiProxy
}

func (b *Broker) configureOpenAIProxySupervisorCallbacks(sup *supervisor) {
	sup.onCrash, sup.onRecovered = b.supervisedWorkerCallbacks("openai-proxy", func() { b.setOpenAIProxy(nil) })
	sup.onExhausted = func(attempt int) {
		slog.Warn("openai-proxy is terminally unavailable; OpenAI-compatible routing is off",
			"attempt", attempt)
	}
}

// spawnOpenAIProxy is the openai-proxy supervisor's spawn closure. It threads
// the cluster dir so the proxy brings up its pin-gated LAN mTLS ingress (and
// dials peers over mTLS) once this node is clustered.
func (b *Broker) spawnOpenAIProxy() (supervisedHandle, error) {
	generation := b.openaiProxyGeneration.Add(1)
	pp, err := startProxy(
		"openai-proxy",
		b.openaiProxyPath,
		applog.LevelString(),
		b.relayDir,
		func(method string, params json.RawMessage) {
			b.forwardOpenAIProxyNotificationForGeneration(generation, method, params)
		},
		b.clusterDirArgs()...,
	)
	if err != nil {
		return nil, err
	}
	b.setOpenAIProxy(pp)
	b.openaiProxyPublishedGeneration.Store(generation)
	slog.Info("openai-proxy started", "path", b.openaiProxyPath, "pid", pp.cmd.Process.Pid)
	return pp, nil
}

func (b *Broker) forwardOpenAIProxyNotification(method string, params json.RawMessage) {
	b.forwardOpenAIProxyNotificationForGeneration(b.openaiProxyGeneration.Load(), method, params)
}

// forwardOpenAIProxyNotificationForGeneration is the hook startProxy invokes on
// the openai-proxy reader goroutine, mirroring its two siblings: errors:report /
// errors:clear go into the nvpair-errors pipeline, workload lifecycle events are
// stamped and forwarded to the workload-manager for cluster broadcast, and
// everything else is re-emitted to openai-proxy:subscribe'd clients as
// openai-proxy:<method>.
func (b *Broker) forwardOpenAIProxyNotificationForGeneration(generation uint64, method string, params json.RawMessage) {
	if b.openaiProxyGeneration.Load() != generation {
		return
	}
	if b.dispatchErrorsNotif("openai-proxy", method, params) {
		return
	}
	if method == "error" {
		var ep struct {
			Code string `json:"code"`
			Port int    `json:"port"`
		}
		if json.Unmarshal(params, &ep) == nil && ep.Code == "bind-failed" {
			// Deliberately no fallback port: see the file comment. Clients are
			// configured against this address, so a silent move is worse than a
			// visible failure.
			slog.Error("openai-proxy could not bind its port; OpenAI-compatible routing is unavailable",
				"port", ep.Port,
				"hint", "free the port, or set another one with openai-proxy:set-port")
		}
	}
	if proxyWorkloadMethods[method] {
		b.routeProxyWorkload(method, params)
		return
	}
	if method == noderec.NotifyNodeActivity {
		b.routeNodeActivity(params)
		return
	}
	b.proxyMu.Lock()
	subscribed := b.openaiProxySubscribed
	b.proxyMu.Unlock()
	if !subscribed {
		return
	}
	if err := b.codec.Notify("openai-proxy:"+method, params); err != nil {
		slog.Warn("forward openai-proxy notification failed", "method", method, "err", err)
	}
}

// relayToOpenAIProxy forwards an openai-proxy:<method> request to openai-proxy
// as <method> (prefix stripped) and maps its response straight back.
// openai-proxy:shutdown is refused — the broker owns the proxy's lifecycle.
func (b *Broker) relayToOpenAIProxy(msg *Message) {
	method := strings.TrimPrefix(msg.Method, "openai-proxy:")
	if method == "shutdown" {
		if err := b.codec.RespondError(msg.ID, -32601, "openai-proxy:shutdown is not allowed; the broker owns the proxy lifecycle"); err != nil {
			log.Printf("failed to respond to openai-proxy:shutdown: %v", err)
		}
		return
	}

	p := b.getOpenAIProxy()
	if p == nil {
		if err := b.codec.RespondError(msg.ID, -32000, "openai-proxy not available"); err != nil {
			log.Printf("failed to respond to %s: %v", msg.Method, err)
		}
		return
	}

	result, rpcErr, err := p.Call(context.Background(), method, msg.Params)
	switch {
	case err != nil:
		if err := b.codec.RespondError(msg.ID, -32000, fmt.Sprintf("openai-proxy call failed: %v", err)); err != nil {
			log.Printf("failed to respond to %s: %v", msg.Method, err)
		}
	case rpcErr != nil:
		if err := b.codec.RespondError(msg.ID, rpcErr.Code, rpcErr.Message); err != nil {
			log.Printf("failed to relay openai-proxy error for %s: %v", msg.Method, err)
		}
	default:
		if err := b.codec.Respond(msg.ID, result); err != nil {
			log.Printf("failed to relay openai-proxy result for %s: %v", msg.Method, err)
		}
	}
}

// openaiProxyListenPort is the OpenAI sibling of proxyListenPort: the port
// openai-proxy actually bound, or 0 when it is not up yet.
func (b *Broker) openaiProxyListenPort() int {
	if p := b.getOpenAIProxy(); p != nil {
		if ready, port := p.Status(); ready && port > 0 {
			return port
		}
	}
	return 0
}

// runAutoAdvertiseOpenAI is the OpenAI sibling of runAutoAdvertise: it keeps the
// oa advertisement and the per-engine local backends in step with what
// engine-manager reports, on the same interval as the other two.
func (b *Broker) runAutoAdvertiseOpenAI(ctx context.Context) {
	ticker := time.NewTicker(autoAdvertiseInterval)
	defer ticker.Stop()

	b.refreshOpenAIAdvertisement()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.refreshOpenAIAdvertisement()
		}
	}
}

// refreshOpenAIAdvertisement advertises this node's OpenAI facade and pushes one
// local backend per engine.
//
// Unlike the Ollama/LM Studio pollers there is no "did the engine move" branch:
// these engines are externally managed, so their ports come from engine-manager
// and PAIR never changes them. The service is advertised when the proxy is up
// and at least one engine is actually serving — advertising with nothing behind
// it would make this node a routing candidate that can only return 503.
func (b *Broker) refreshOpenAIAdvertisement() {
	proxyPort := b.openaiProxyListenPort()
	p := b.getOpenAIProxy()
	anyHealthy := false

	for _, engine := range openaiEngines {
		enginePort, probe := b.localEnginePort(engine, 0)
		// An engine on the proxy's own port cannot be told apart from the proxy
		// itself, and pointing the facade at its own listener would make it
		// forward to itself.
		up := probe && enginePort > 0 && proxyPort != 0 && enginePort != proxyPort
		b.setProxyLocalBackend(p, engine, enginePort, up)
		if up {
			anyHealthy = true
		}
	}

	if anyHealthy {
		b.registerService(noderec.RegisterParams{Service: noderec.ServiceOpenAI, Port: proxyPort})
	} else {
		b.unregisterService(noderec.ServiceOpenAI)
	}
}
