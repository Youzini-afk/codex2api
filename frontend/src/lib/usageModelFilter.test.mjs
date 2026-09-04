import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { extractBalancedBody } from './sourceBoundary.mjs'

const usageSource = readFileSync(new URL('../pages/Usage.tsx', import.meta.url), 'utf8')

test('Usage model filter keeps Antigravity and Claude catalogs available without logs', () => {
  const loadModels = extractBalancedBody(usageSource, 'const loadModels = async')
  assert.equal(loadModels.includes('setAntigravityModelOptions(response.antigravity_models ?? [])'), true)
  assert.equal(loadModels.includes('setClaudeModelOptions(response.claude_models ?? [])'), true)

  const modelFilterOptions = extractBalancedBody(usageSource, 'const modelFilterOptions = useMemo(() =>')
  assert.equal(modelFilterOptions.includes("channel === 'antigravity'"), true)
  assert.equal(modelFilterOptions.includes("channel === 'claude'"), true)
  assert.equal(modelFilterOptions.includes('antigravityModelOptions'), true)
  assert.equal(modelFilterOptions.includes('claudeModelOptions'), true)
  assert.equal(modelFilterOptions.includes('[...modelOptions, ...grokModelOptions, ...antigravityModelOptions, ...claudeModelOptions]'), true)
  assert.equal(modelFilterOptions.includes('for (const item of modelStats)'), true)
  assert.equal(modelFilterOptions.includes('return merged'), true)
})
