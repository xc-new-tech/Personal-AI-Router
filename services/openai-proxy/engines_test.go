// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"nvpair-shared/noderec"
)

func TestEngineForModel(t *testing.T) {
	node := Node{
		ID:     "n1",
		Models: []string{"qwen-mlx", "qwen-vllm"},
		ModelsByEngine: map[string][]string{
			"omlx": {"qwen-mlx", "shared"},
			"vllm": {"qwen-vllm", "shared"},
		},
	}

	if got := engineForModel(node, "qwen-vllm"); got != "vllm" {
		t.Errorf("engineForModel(qwen-vllm) = %q, want vllm", got)
	}
	// Both engines hold "shared": openaiEngines order decides, and oMLX leads
	// because it is the one with a persistent KV cache.
	if got := engineForModel(node, "shared"); got != "omlx" {
		t.Errorf("engineForModel(shared) = %q, want omlx (preference order)", got)
	}
	// Unknown must stay unknown — callers fall back rather than guess.
	if got := engineForModel(node, "not-here"); got != "" {
		t.Errorf("engineForModel(not-here) = %q, want empty", got)
	}
	if got := engineForModel(Node{Models: []string{"m"}}, "m"); got != "" {
		t.Errorf("a node with no attribution must yield %q, got %q", "", got)
	}
}

func TestAttributionForKeepsOnlyOurEngines(t *testing.T) {
	dn := noderec.DirectoryNode{
		Models: []string{"a", "b", "c"},
		ModelsByEngine: map[string][]string{
			"omlx":   {"a"},
			"ollama": {"b"}, // another facade's engine: must not leak in
			"vllm":   {"c"},
		},
	}
	got := attributionFor(dn, openaiEngines)
	want := map[string][]string{"omlx": {"a"}, "vllm": {"c"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("attributionFor = %v, want %v", got, want)
	}
	if attributionFor(noderec.DirectoryNode{Models: []string{"a"}}, openaiEngines) != nil {
		t.Error("a node with no attribution must yield nil, the signal to fall back")
	}
}

// A client must not be able to steer which engine a peer serves from, so the
// header is only honored for engines this facade actually fronts.
func TestEngineFromRequestRejectsUnknown(t *testing.T) {
	for _, tc := range []struct{ header, want string }{
		{"omlx", "omlx"},
		{"sglang", "sglang"},
		{"ollama", ""}, // real engine, but not one of ours
		{"", ""},
		{"../etc", ""},
	} {
		r := &http.Request{Header: http.Header{}}
		if tc.header != "" {
			r.Header.Set(engineHeader, tc.header)
		}
		if got := engineFromRequest(r); got != tc.want {
			t.Errorf("engineFromRequest(%q) = %q, want %q", tc.header, got, tc.want)
		}
	}
}

func TestLocalBackendsArePerEngine(t *testing.T) {
	p := &Proxy{}
	p.setLocalBackend(localBackend{Engine: "omlx", Port: 8123, Healthy: true})
	p.setLocalBackend(localBackend{Engine: "vllm", Port: 8000, Healthy: true})

	for engine, wantPort := range map[string]string{"omlx": "8123", "vllm": "8000"} {
		u, ok := p.localBackendFor(engine)
		if !ok {
			t.Fatalf("%s backend missing", engine)
		}
		if u.Host != "127.0.0.1:"+wantPort {
			t.Errorf("%s target = %s, want 127.0.0.1:%s", engine, u.Host, wantPort)
		}
	}
	if _, ok := p.localBackendFor("sglang"); ok {
		t.Error("an unset engine must not resolve to another engine's backend")
	}
}

// With more than one healthy backend there is no safe arbitrary pick: answering
// from the wrong engine would serve a different model than the caller ranked.
func TestSoleHealthyBackendGivesUpWhenAmbiguous(t *testing.T) {
	p := &Proxy{}
	if _, _, ok := p.soleHealthyBackend(); ok {
		t.Error("no backends must not resolve")
	}

	p.setLocalBackend(localBackend{Engine: "omlx", Port: 8123, Healthy: true})
	u, engine, ok := p.soleHealthyBackend()
	if !ok || u.Host != "127.0.0.1:8123" {
		t.Fatalf("single healthy backend = %v/%v, want 127.0.0.1:8123", u, ok)
	}
	// The engine comes back too, so a model-list hop (which names no model and
	// so cannot derive the engine from attribution) can still be authorized.
	if engine != "omlx" {
		t.Errorf("engine = %q, want omlx", engine)
	}

	p.setLocalBackend(localBackend{Engine: "vllm", Port: 8000, Healthy: true})
	if _, _, ok := p.soleHealthyBackend(); ok {
		t.Error("two healthy backends must be reported ambiguous, not guessed")
	}

	// An unhealthy second backend is not a real choice, so the first one still wins.
	p.setLocalBackend(localBackend{Engine: "vllm", Port: 8000, Healthy: false})
	if u, engine, ok := p.soleHealthyBackend(); !ok || u.Host != "127.0.0.1:8123" || engine != "omlx" {
		t.Errorf("unhealthy peer backend must not create ambiguity, got %v/%v/%v", u, engine, ok)
	}
}

func TestBackendURLRejectsUnusable(t *testing.T) {
	for _, b := range []localBackend{
		{Engine: "omlx", Port: 0, Healthy: true},
		{Engine: "omlx", Port: -1, Healthy: true},
		{Engine: "omlx", Port: 8123, Healthy: false},
	} {
		if _, ok := backendURL(b); ok {
			t.Errorf("backendURL(%+v) must not resolve", b)
		}
	}
	u, ok := backendURL(localBackend{Engine: "omlx", Host: "", Port: 8123, Healthy: true})
	if !ok || u.Scheme != "http" || u.Host != "127.0.0.1:8123" {
		t.Errorf("empty host must default to loopback, got %v", (*url.URL)(u))
	}
}
