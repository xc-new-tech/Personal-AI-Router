// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// externalManifest is a minimal well-formed external-mode manifest the
// validation tests mutate a field at a time.
func externalManifest(t *testing.T, mutate func(m map[string]any)) []byte {
	t.Helper()
	m := map[string]any{
		"engine":           "testext",
		"display_name":     "Test External",
		"manifest_version": 1,
		"platforms": map[string]any{
			"darwin/arm64": map[string]any{
				"runtime": map[string]any{
					"mode": "external",
					"port": 9999,
					"health": map[string]any{
						"http": "http://127.0.0.1:{port}/v1/models",
					},
				},
			},
		},
	}
	if mutate != nil {
		mutate(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func platformRuntime(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	plats := m["platforms"].(map[string]any)
	plat := plats["darwin/arm64"].(map[string]any)
	return plat["runtime"].(map[string]any)
}

func TestExternalModeValidates(t *testing.T) {
	reg := NewRegistry()
	if _, err := reg.addManifest("test", externalManifest(t, nil)); err != nil {
		t.Fatalf("a well-formed external manifest must load: %v", err)
	}
	m, ok := reg.Get("testext")
	if !ok {
		t.Fatal("engine not registered")
	}
	plat, ok := m.PlatformFor("darwin", "arm64")
	if !ok {
		t.Fatal("platform not found")
	}
	if got := plat.Runtime.modeOrDefault(); got != "external" {
		t.Fatalf("mode = %q, want external", got)
	}
}

// External mode owns no lifecycle. A manifest that also declares how to launch
// or install the engine is contradicting itself, so it must be rejected rather
// than silently ignored — otherwise an operator reads the manifest and expects
// PAIR to manage a service it will never touch.
func TestExternalModeRejectsLifecycleSpecs(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(m map[string]any)
		want   string
	}{
		{
			name:   "bin",
			mutate: func(m map[string]any) { platformRuntime(t, m)["bin"] = "/usr/bin/whatever" },
			want:   "not allowed in external mode",
		},
		{
			name:   "start",
			mutate: func(m map[string]any) { platformRuntime(t, m)["start"] = [][]string{{"serve"}} },
			want:   "not allowed in external mode",
		},
		{
			name: "install",
			mutate: func(m map[string]any) {
				plats := m["platforms"].(map[string]any)
				plat := plats["darwin/arm64"].(map[string]any)
				plat["install"] = map[string]any{"fetch": map[string]any{"url": "https://example.com/x.zip"}}
			},
			want: "install/uninstall are not allowed in external mode",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRegistry()
			_, err := reg.addManifest("test", externalManifest(t, tc.mutate))
			if err == nil {
				t.Fatalf("expected rejection, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// Without a process handle the probe is the only liveness signal, so an
// external engine with no probe could never be seen as running.
func TestExternalModeRequiresProbe(t *testing.T) {
	reg := NewRegistry()
	_, err := reg.addManifest("test", externalManifest(t, func(m map[string]any) {
		delete(platformRuntime(t, m), "health")
	}))
	if err == nil {
		t.Fatal("expected rejection when neither ready nor health is set")
	}
	if !strings.Contains(err.Error(), "requires runtime.ready or runtime.health") {
		t.Fatalf("error = %q", err)
	}
}

func TestEngineAPIKeyEnvName(t *testing.T) {
	for _, tc := range []struct{ engine, want string }{
		{"omlx", "NVPAIR_OMLX_API_KEY"},
		{"lm-studio", "NVPAIR_LM_STUDIO_API_KEY"},
		{"sglang", "NVPAIR_SGLANG_API_KEY"},
	} {
		if got := engineAPIKeyEnv(tc.engine); got != tc.want {
			t.Errorf("engineAPIKeyEnv(%q) = %q, want %q", tc.engine, got, tc.want)
		}
	}
}

// The credential must come from the environment, never from the manifest: the
// bundled manifests are compiled into the binary and live in version control.
func TestAPIKeyResolvesFromEnv(t *testing.T) {
	t.Setenv("NVPAIR_TESTEXT_API_KEY", "s3cret")

	reg := NewRegistry()
	if _, err := reg.addManifest("test", externalManifest(t, func(m map[string]any) {
		platformRuntime(t, m)["health"] = map[string]any{
			"http":    "http://127.0.0.1:{port}/v1/models",
			"headers": map[string]any{"Authorization": "Bearer {api_key}"},
		}
	})); err != nil {
		t.Fatalf("load: %v", err)
	}
	m, _ := reg.Get("testext")
	plat, _ := m.PlatformFor("darwin", "arm64")
	if got := plat.Runtime.Health.Headers["Authorization"]; got != "Bearer s3cret" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer s3cret")
	}
}

// An unresolvable credential drops the header rather than sending an empty
// bearer token: a blank credential and a wrong one both return 401, and only
// the dropped-header log says which it was.
func TestAPIKeyMissingDropsHeader(t *testing.T) {
	t.Setenv("NVPAIR_TESTEXT_API_KEY", "")

	reg := NewRegistry()
	if _, err := reg.addManifest("test", externalManifest(t, func(m map[string]any) {
		platformRuntime(t, m)["health"] = map[string]any{
			"http": "http://127.0.0.1:{port}/v1/models",
			"headers": map[string]any{
				"Authorization": "Bearer {api_key}",
				"X-Static":      "kept",
			},
		}
	})); err != nil {
		t.Fatalf("load: %v", err)
	}
	m, _ := reg.Get("testext")
	plat, _ := m.PlatformFor("darwin", "arm64")
	h := plat.Runtime.Health.Headers
	if _, ok := h["Authorization"]; ok {
		t.Error("Authorization must be dropped when the credential is unset")
	}
	if h["X-Static"] != "kept" {
		t.Error("a header without {api_key} must survive untouched")
	}
}

// The bundled oMLX manifest is the first shipped external-mode engine; keep its
// shape honest so a regression in the schema surfaces here rather than in the UI.
func TestBundledOMLXManifest(t *testing.T) {
	reg := NewRegistry()
	if err := reg.LoadFS(bundledManifests, "manifests"); err != nil {
		t.Fatalf("load bundled manifests: %v", err)
	}
	m, ok := reg.Get("omlx")
	if !ok {
		t.Fatal("omlx manifest is not bundled")
	}
	plat, ok := m.PlatformFor("darwin", "arm64")
	if !ok {
		t.Fatal("omlx must declare darwin/arm64 (oMLX is Apple Silicon only)")
	}
	if got := plat.Runtime.modeOrDefault(); got != "external" {
		t.Errorf("mode = %q, want external", got)
	}
	if plat.Runtime.Port != 8123 {
		t.Errorf("port = %d, want 8123", plat.Runtime.Port)
	}
	act, ok := m.Actions["list_models"]
	if !ok {
		t.Fatal("list_models action missing")
	}
	if act.Result == nil || act.Result.Array != "data" || act.Result.Field != "id" {
		t.Errorf("list_models result = %+v, want the OpenAI {data:[{id}]} shape", act.Result)
	}
}

// vLLM and SGLang are the externally managed engines on the Linux nodes. They
// share oMLX's OpenAI surface, so the same declarative extractor serves all
// three — this asserts the shared shape rather than restating each manifest.
func TestBundledExternalEngines(t *testing.T) {
	reg := NewRegistry()
	if err := reg.LoadFS(bundledManifests, "manifests"); err != nil {
		t.Fatalf("load bundled manifests: %v", err)
	}
	for _, tc := range []struct {
		engine    string
		port      int
		platforms []string
	}{
		{"omlx", 8123, []string{"darwin/arm64"}},
		{"vllm", 8000, []string{"linux/amd64", "linux/arm64", "darwin/arm64"}},
		{"sglang", 30000, []string{"linux/amd64", "linux/arm64"}},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			m, ok := reg.Get(tc.engine)
			if !ok {
				t.Fatalf("%s manifest is not bundled", tc.engine)
			}
			for _, key := range tc.platforms {
				parts := strings.SplitN(key, "/", 2)
				plat, ok := m.PlatformFor(parts[0], parts[1])
				if !ok {
					t.Errorf("missing platform %s", key)
					continue
				}
				if got := plat.Runtime.modeOrDefault(); got != "external" {
					t.Errorf("%s mode = %q, want external", key, got)
				}
				if plat.Runtime.Port != tc.port {
					t.Errorf("%s port = %d, want %d", key, plat.Runtime.Port, tc.port)
				}
				// External mode has no process handle, so a missing probe would
				// leave the engine permanently invisible.
				if plat.Runtime.Ready == nil || plat.Runtime.Health == nil {
					t.Errorf("%s must declare both ready and health probes", key)
				}
			}
			act, ok := m.Actions["list_models"]
			if !ok {
				t.Fatalf("%s: list_models action missing", tc.engine)
			}
			if act.Result == nil || act.Result.Array != "data" || act.Result.Field != "id" {
				t.Errorf("%s: list_models result = %+v, want the OpenAI {data:[{id}]} shape", tc.engine, act.Result)
			}
		})
	}
}
