// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { EngineType, ModelExpiry } from '@/shared/types/engines'

// The engines `nvpair-engine-manager` ships a manifest for, and therefore the
// only ones PAIR can install, run or route to. llama-cpp, whisper-cpp,
// piper-tts, sherpa-onnx-tts and stable-diffusion-cpp were carried here as
// never-enabled placeholders; they were removed with the chat window, which was
// their only in-app consumer. Adding an engine back means shipping its manifest
// first -- an engine row without one renders commands that fail with `-32000`.
//
// oMLX, vLLM and SGLang ship external-mode manifests (runtime.mode "external"):
// PAIR discovers, probes and lists them, but never installs, starts or stops
// them, because their lifecycle belongs to a menu-bar app or to the host's own
// service manager. Their ids match the engine-manager engine names exactly, so
// unlike lm-studio they need no entry in the id translation.
export const EngineTypes = ['ollama', 'lm-studio', 'omlx', 'vllm', 'sglang'] as const

// Kept as a distinct export so a future engine can ship behind it rather than
// appearing the moment its type exists.
export const EnabledEngineTypes: EngineType[] = [
    'ollama',
    'lm-studio',
    'omlx',
    'vllm',
    'sglang'
] as const

// ExternallyManagedEngines are the engines whose manifests use runtime.mode
// "external". PAIR must not offer Install/Start/Stop for them: engine-manager
// refuses those operations by design, so an enabled button could only produce
// an error the user has no way to act on.
export const ExternallyManagedEngines: EngineType[] = ['omlx', 'vllm', 'sglang'] as const

/** True when PAIR observes an engine's lifecycle but does not control it. */
export function isExternallyManaged(engine: EngineType): boolean {
    return ExternallyManagedEngines.includes(engine)
}

export const EngineSources = ['bundled', 'detected', 'installed'] as const

export const EngineDisplayNames: Record<EngineType, string> = {
    ollama: 'Ollama',
    'lm-studio': 'LM Studio',
    omlx: 'oMLX',
    vllm: 'vLLM',
    sglang: 'SGLang'
} as const

/** Default docs/install URLs for built-in backends. Single source of truth for UI and adapter buildInfo(). */
export const EngineDefaultLinks: Record<EngineType, { docsUrl: string; installUrl: string }> = {
    ollama: { docsUrl: 'https://docs.ollama.com/', installUrl: 'https://ollama.com/download' },
    'lm-studio': { docsUrl: 'https://lmstudio.ai/docs', installUrl: 'https://lmstudio.ai/' },
    // An externally managed engine has no in-app install path, so installUrl is
    // where the user obtains it themselves rather than something PAIR drives.
    omlx: { docsUrl: 'https://omlx.ai/', installUrl: 'https://github.com/jundot/omlx/releases' },
    vllm: {
        docsUrl: 'https://docs.vllm.ai/',
        installUrl: 'https://docs.vllm.ai/en/latest/getting_started/installation.html'
    },
    sglang: {
        docsUrl: 'https://docs.sglang.ai/',
        installUrl: 'https://docs.sglang.ai/start/install.html'
    }
} as const

export const ModelItemStatuses = ['idle', 'loading', 'loaded', 'ejecting', 'pulling'] as const

export const ModelExpiries = ['0', '1s', '10s', '1m', '10m', '-1'] as const

export const ModelExpiryLabels: Record<ModelExpiry, string> = {
    '0': 'Immediately',
    '1s': '1 second',
    '10s': '10 seconds',
    '1m': '1 minute',
    '10m': '10 minutes',
    '-1': 'Never'
} as const

export const EngineProcessStatuses = [
    'running',
    'stopped',
    'not-installed',
    'installing',
    'uninstalling',
    'starting',
    'stopping',
    'initializing'
] as const

/**
 * Engine progress domain -- delivered from the backend as progress push events.
 * Unified progress channel for all operations: install, pull, load, etc.
 * Keyed by ${nodeId}:${engineType}:${operation}:${model?} for concurrent ops.
 */

export const EngineOperationTypes = [
    'install',
    'uninstall',
    'pull',
    'load',
    'unload',
    'delete'
] as const
