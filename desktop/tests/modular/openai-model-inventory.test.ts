// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

/**
 * The externally managed engines (oMLX, vLLM, SGLang) answer list_models with
 * the OpenAI envelope `{ data: [{ id }] }` rather than the `{ models: [...] }`
 * shape Ollama and LM Studio use. The broker parses the engine's raw action
 * result, so it has to recognize that shape too — without this, oMLX reported
 * "list_models response is missing its model array" while its four models were
 * plainly present in the response.
 */

import { describe, expect, it } from 'vitest'

import { parseListModelNames } from '@/electron/service-bridge/modular-supervisor'

describe('OpenAI-compatible model inventory', () => {
    it('parses the OpenAI {data:[{id}]} envelope', () => {
        expect(
            parseListModelNames({
                object: 'list',
                data: [
                    { id: 'Qwen3.8-27B-4bit', object: 'model', owned_by: 'omlx' },
                    { id: 'Qwen3.5-4B-MLX-4bit', object: 'model', owned_by: 'omlx' }
                ]
            })
        ).toEqual(['Qwen3.8-27B-4bit', 'Qwen3.5-4B-MLX-4bit'])
    })

    it('treats an explicitly empty data array as authoritative, not unknown', () => {
        expect(parseListModelNames({ object: 'list', data: [] })).toEqual([])
    })

    it('still rejects a response carrying neither envelope', () => {
        expect(() => parseListModelNames({ object: 'list' })).toThrow('missing its model array')
        expect(() => parseListModelNames({ data: null })).toThrow('missing its model array')
    })

    it('rejects a data array with no usable ids rather than reporting no models', () => {
        expect(() => parseListModelNames({ data: [{ object: 'model' }] })).toThrow(
            'no usable model names'
        )
    })

    it('keeps the Ollama and LM Studio shapes working', () => {
        expect(parseListModelNames({ models: [{ name: 'qwen4:12b' }] })).toEqual(['qwen4:12b'])
        expect(parseListModelNames({ models: [{ key: 'lmstudio-community/phi-3' }] })).toEqual([
            'lmstudio-community/phi-3'
        ])
    })
})
