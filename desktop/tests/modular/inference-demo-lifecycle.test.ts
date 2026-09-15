// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * Lifecycle guarantees for the Inference Demo scheduler.
 *
 * The pure schedule is covered in `inference-demo-schedule.test.ts`; this file
 * covers the parts the spec is strictest about and that are easiest to get
 * wrong: the 60 second ceiling under a stalled clock, that Stop never signals an
 * already-spawned process, that every request goes to a proxy facade rather than
 * an engine backend, and that the run always returns to idle so the toast cannot
 * get wedged open.
 */

interface ChildOptions {
    env?: NodeJS.ProcessEnv
    stdio?: string
}

interface SpawnRecord {
    args: string[]
    options: ChildOptions
    killed: string[]
    emit: (event: string) => void
}

const spawned: SpawnRecord[] = []

/** Options each `--list-models` discovery probe was invoked with. */
const probeOptions: ChildOptions[] = []

/**
 * Poisoned parent environment. `INFERENCE_DISPATCHER_LOOP` would run the child
 * forever and `_RESULT_LOG` would put inference metadata on disk; the lowercase
 * spelling is here because Windows resolves environment variables
 * case-insensitively, so it reaches a child as `INFERENCE_DISPATCHER_CONFIG`.
 */
const POISONED_ENV: Record<string, string> = {
    INFERENCE_DISPATCHER_LOOP: '1',
    INFERENCE_DISPATCHER_RESULT_LOG: '/tmp/leak.jsonl',
    inference_dispatcher_config: '/tmp/attacker.json',
    Inference_Dispatcher_Debug_Error_Log: '/tmp/leak.log'
}

/** Proxy ports the fake broker reports. Mutable so a test can withhold one. */
const proxyPorts: Record<'ollama' | 'lm-studio', number | null> = {
    ollama: 11434,
    'lm-studio': 1234
}

/**
 * The OpenAI facade's port, reported separately from proxyPorts because it
 * fronts three engines and so has no single ProxyEngine key. Null by default:
 * these tests assert the two-engine baseline, and a third probe would change
 * every expected request count.
 */
let openaiProxyPort: number | null = null

/** Model inventory each probe returns. Mutable so a test can return none. */
let inventory: unknown = [{ name: 'demo-model', type: 'llm' }]

/**
 * Minimal ChildProcess stand-in. Records signals rather than delivering them and
 * lets a test drive 'spawn'/'close'/'error' explicitly, which is how the
 * submission accounting is exercised.
 */
function fakeChild(args: string[], options: ChildOptions): SpawnRecord & { handle: unknown } {
    const listeners = new Map<string, (() => void)[]>()
    const record: SpawnRecord = {
        args,
        options,
        killed: [],
        emit: event => (listeners.get(event) ?? []).forEach(fn => fn())
    }
    return {
        ...record,
        handle: {
            exitCode: null,
            once: (event: string, fn: () => void) => {
                listeners.set(event, [...(listeners.get(event) ?? []), fn])
            },
            kill: (signal: string) => {
                record.killed.push(signal)
                return true
            }
        }
    }
}

vi.mock('node:child_process', () => ({
    spawn: (_executable: string, args: string[], options: ChildOptions) => {
        const child = fakeChild(args, options)
        spawned.push(child)
        // Real spawns report success asynchronously; mirror that so the
        // 'spawn'-based submission counter is genuinely exercised.
        queueMicrotask(() => child.emit('spawn'))
        return child.handle
    },
    execFile: (
        _executable: string,
        _args: string[],
        options: ChildOptions,
        callback: (error: Error | null, stdout: string, stderr: string) => void
    ) => {
        probeOptions.push(options)
        callback(null, JSON.stringify(inventory), '')
        return { exitCode: null, once: () => {}, kill: () => true }
    }
}))

vi.mock('node:fs', () => ({
    default: { existsSync: () => true },
    existsSync: () => true
}))

vi.mock('electron', () => ({
    app: { isPackaged: false, getAppPath: () => '/fake/app' },
    BrowserWindow: { getAllWindows: () => [] }
}))

vi.mock('@/electron/service-bridge/modular-state', () => ({
    getModularBridgeState: () => ({
        getProxyPort: (engine: 'ollama' | 'lm-studio') => proxyPorts[engine],
        getOpenAIProxyPort: () => openaiProxyPort
    })
}))

import {
    destroyInferenceDemoSync,
    getInferenceDemoState,
    startInferenceDemo,
    stopInferenceDemo
} from '@/electron/inference-demo'

/** Ports the demo is allowed to target: proxy facades only. */
const PROXY_FACADE_PORTS = [11434, 1234]

function portOf(record: SpawnRecord): number {
    return Number(record.args[record.args.indexOf('--port') + 1])
}

function modelOf(record: SpawnRecord): string {
    return record.args[record.args.indexOf('--model') + 1] ?? ''
}

beforeEach(() => {
    spawned.length = 0
    probeOptions.length = 0
    inventory = [{ name: 'demo-model', type: 'llm' }]
    proxyPorts.ollama = 11434
    proxyPorts['lm-studio'] = 1234
    openaiProxyPort = null
    Object.assign(process.env, POISONED_ENV)
    vi.useFakeTimers()
})

afterEach(() => {
    // Returns the module to idle with no pending timers, which is what lets
    // every test share one statically imported instance.
    destroyInferenceDemoSync()
    for (const key of Object.keys(POISONED_ENV)) delete process.env[key]
    vi.useRealTimers()
})

describe('inference demo lifecycle', () => {
    it('returns to idle immediately on stop, with no draining tail', async () => {
        await startInferenceDemo()
        expect(getInferenceDemoState().status).toBe('running')

        await vi.advanceTimersByTimeAsync(12_000)
        expect(spawned.length).toBeGreaterThan(0)

        expect(stopInferenceDemo().status).toBe('idle')
        expect(getInferenceDemoState().status).toBe('idle')
    })

    it('never signals an already-submitted request on stop', async () => {
        await startInferenceDemo()
        await vi.advanceTimersByTimeAsync(12_000)

        const before = spawned.length
        stopInferenceDemo()
        await vi.advanceTimersByTimeAsync(120_000)

        expect(spawned.flatMap(child => child.killed)).toEqual([])
        expect(spawned.length).toBe(before)
    })

    it('ends at idle once the schedule completes', async () => {
        await startInferenceDemo()
        await vi.advanceTimersByTimeAsync(70_000)
        expect(getInferenceDemoState().status).toBe('idle')
    })

    it('drops expired timers instead of flushing a burst past the ceiling', async () => {
        await startInferenceDemo()
        const atStart = spawned.length

        // Simulate a suspended machine: the wall clock jumps past the ceiling
        // before any pending timer runs.
        vi.setSystemTime(new Date(Date.now() + 10 * 60 * 1000))
        await vi.advanceTimersByTimeAsync(70_000)

        expect(getInferenceDemoState().status).toBe('idle')
        expect(spawned.length).toBe(atStart)
    })

    it('only ever targets a proxy facade, never an engine backend port', async () => {
        await startInferenceDemo()
        await vi.advanceTimersByTimeAsync(70_000)

        expect(spawned.length).toBeGreaterThan(0)
        for (const child of spawned) {
            expect(PROXY_FACADE_PORTS).toContain(portOf(child))
        }
    })

    // Regression: the demo only knew the two per-engine facades, so a node whose
    // only engine was oMLX/vLLM/SGLang was told to "Start Ollama or LM Studio"
    // even while PAIR was routing inference to it perfectly well.
    it('targets the OpenAI facade once it reports a port', async () => {
        openaiProxyPort = 11436
        await startInferenceDemo()
        await vi.advanceTimersByTimeAsync(70_000)

        expect(spawned.map(portOf)).toContain(11436)
    })

    it('runs on the OpenAI facade alone when it is the only one up', async () => {
        proxyPorts.ollama = null
        proxyPorts['lm-studio'] = null
        openaiProxyPort = 11436
        await startInferenceDemo()
        await vi.advanceTimersByTimeAsync(70_000)

        expect(spawned.length).toBeGreaterThan(0)
        for (const child of spawned) {
            expect(portOf(child)).toBe(11436)
        }
    })

    // Regression: a plain OpenAI /v1/models response carries no capability
    // field, so embedding models used to pass the "no metadata, assume yes"
    // check and collect HTTP 400s from every chat completion aimed at them.
    it('does not target an embedding model that declares no capabilities', async () => {
        inventory = [{ name: 'Qwen3-Embedding-4B-4bit-DWQ' }, { name: 'Qwen3.5-4B-MLX-4bit' }]
        await startInferenceDemo()
        await vi.advanceTimersByTimeAsync(70_000)

        expect(spawned.length).toBeGreaterThan(0)
        for (const child of spawned) {
            expect(modelOf(child)).toBe('Qwen3.5-4B-MLX-4bit')
        }
    })

    // The heuristic is only for the unannotated case; a declared type wins.
    it('still targets a declared llm whose name looks like an embedding', async () => {
        inventory = [{ name: 'weird-embed-name', type: 'llm' }]
        await startInferenceDemo()
        await vi.advanceTimersByTimeAsync(70_000)

        expect(spawned.length).toBeGreaterThan(0)
        for (const child of spawned) {
            expect(modelOf(child)).toBe('weird-embed-name')
        }
    })

    it('skips an engine whose proxy has not reported a port', async () => {
        proxyPorts['lm-studio'] = null
        await startInferenceDemo()
        await vi.advanceTimersByTimeAsync(70_000)

        expect(spawned.length).toBeGreaterThan(0)
        for (const child of spawned) {
            expect(portOf(child)).toBe(11434)
        }
    })

    it('rejects when no engine exposed a text-generation model', async () => {
        inventory = []
        await expect(startInferenceDemo()).rejects.toThrow(/no local inference engine/i)
        expect(getInferenceDemoState().status).toBe('idle')
    })

    it('counts a submission only once the process actually started', async () => {
        await startInferenceDemo()
        await vi.advanceTimersByTimeAsync(12_000)
        expect(getInferenceDemoState().submitted).toBe(spawned.length)
    })

    it('rejects a second demo while one is running', async () => {
        await startInferenceDemo()
        await expect(startInferenceDemo()).rejects.toThrow(/already running/i)
    })

    it('allows a new demo once the previous one has ended', async () => {
        await startInferenceDemo()
        stopInferenceDemo()
        await expect(startInferenceDemo()).resolves.toMatchObject({ status: 'running' })
    })

    it('never exposes prompts or responses in the broadcast state', async () => {
        await startInferenceDemo()
        await vi.advanceTimersByTimeAsync(12_000)
        expect(Object.keys(getInferenceDemoState()).sort()).toEqual([
            'engineCount',
            'planned',
            'startedAt',
            'status',
            'submitted',
            'targetCount'
        ])
    })

    it('strips the INFERENCE_DISPATCHER_ namespace from every child, in any case', async () => {
        await startInferenceDemo()
        await vi.advanceTimersByTimeAsync(12_000)
        expect(spawned.length).toBeGreaterThan(0)
        expect(probeOptions.length).toBeGreaterThan(0)

        // Discovery probes and submissions are equally able to be redirected, so
        // both paths must scrub. A surviving variable is invisible in argv, which
        // is why the environment itself is asserted rather than just `--loop`.
        for (const options of [...probeOptions, ...spawned.map(child => child.options)]) {
            const passed = Object.keys(options.env ?? {})
            expect(passed.length).toBeGreaterThan(0)
            expect(
                passed.filter(key => key.toLowerCase().startsWith('inference_dispatcher_'))
            ).toEqual([])
            // The rest of the parent environment still reaches the child; only
            // the dispatcher namespace is removed.
            expect(passed).toContain('PATH' in process.env ? 'PATH' : 'Path')
        }

        for (const child of spawned) {
            expect(child.args).not.toContain('--loop')
            // No pipes: the child's stdout would otherwise carry response
            // digests and byte counts into the Electron main process.
            expect(child.options.stdio).toBe('ignore')
        }
    })

    it('kills in-flight requests only at app teardown', async () => {
        await startInferenceDemo()
        await vi.advanceTimersByTimeAsync(12_000)
        expect(spawned.flatMap(child => child.killed)).toEqual([])

        destroyInferenceDemoSync()
        expect(spawned.flatMap(child => child.killed)).toContain('SIGKILL')
        expect(getInferenceDemoState().status).toBe('idle')
    })
})
