import SpeedTest from '@cloudflare/speedtest'
import type { ConfigOptions } from '@cloudflare/speedtest'
import {
  CLOUDFLARE_POC_PROFILE,
  CloudflareSpeedTestProvider,
  type CloudflareEngineLike,
  type CloudflareProgress,
  type CloudflareProviderResult,
} from './providers/cloudflareSpeedtest'

export type P0GScenario =
  | 'normal'
  | 'per_request_timeout'
  | 'overall_timeout'
  | 'cancel'
  | 'close'

interface P0GStartAck {
  started: boolean
  scenario: P0GScenario | null
  reason?: string
}

interface P0GSettledOutcome {
  result: CloudflareProviderResult
  progressCountAtSettle: number
  fetchRestored: boolean
  providerRunning: boolean
  scenario: P0GScenario
}

interface P0GLateCallbackOutcome {
  before: number
  after: number
  ignored: boolean
}

export interface P0GHarness {
  version: 'p0g-1'
  start(scenario: P0GScenario): P0GStartAck
  run(scenario: P0GScenario): Promise<P0GSettledOutcome>
  awaitResult(): Promise<P0GSettledOutcome>
  cancel(): boolean
  dispose(): boolean
  triggerLateCallbacks(): Promise<P0GLateCallbackOutcome>
  getState(): {
    scenario: P0GScenario | null
    running: boolean
    progressCount: number
    lastStage: string | null
  }
}

const scenarios = new Set<P0GScenario>([
  'normal',
  'per_request_timeout',
  'overall_timeout',
  'cancel',
  'close',
])

let provider: CloudflareSpeedTestProvider | null = null
let activeScenario: P0GScenario | null = null
let completion: Promise<P0GSettledOutcome> | null = null
let progressEvents: CloudflareProgress[] = []
let lastEngine: CloudflareEngineLike | null = null
let originalFetch: typeof fetch | null = null
let nativeFetch: typeof fetch | null = null
let controlLoopStarted = false

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

function mockBase(): string {
  const configured = import.meta.env.VITE_CLOUDFLARE_MOCK_BASE_URL?.trim()
  if (!configured) {
    throw new Error('VITE_CLOUDFLARE_MOCK_BASE_URL is required for the P0-G harness')
  }
  return configured.endsWith('/') ? configured : `${configured}/`
}

function mockEndpoint(path: '/__down' | '/__up', scenario: P0GScenario): string {
  const url = new URL(path.slice(1), mockBase())
  url.searchParams.set('scenario', scenario)
  return url.toString()
}

function controlUrl(path: string): string {
  return new URL(path, mockBase()).toString()
}

async function controlFetchJson(path: string): Promise<any> {
  if (!nativeFetch) throw new Error('native fetch is unavailable')
  const response = await nativeFetch(controlUrl(path))
  if (!response.ok) throw new Error(`${path} returned ${response.status}`)
  return response.json()
}

async function waitForInflight(
  scenario: P0GScenario,
  minimumStarted = 1,
  timeoutMs = 20_000,
): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const stats = await controlFetchJson(`__stats?scenario=${encodeURIComponent(scenario)}`)
    if (stats.started >= minimumStarted && stats.inFlight > 0) return
    await sleep(100)
  }
  throw new Error(`timed out waiting for in-flight ${scenario} request`)
}

function createEngine(config: ConfigOptions): CloudflareEngineLike {
  const sdk = new SpeedTest(config)
  const callbacks: {
    onFinish?: (results: any) => void
    onError?: (message: string, status?: number) => void
    onPhaseChange?: (payload: { measurementId: number; measurement: { type: string } }) => void
    onResultsChange?: (payload: { type: string }) => void
  } = {}
  sdk.onFinish = (results) => callbacks.onFinish?.(results)
  sdk.onError = (message, status) => callbacks.onError?.(message, status)
  sdk.onPhaseChange = (payload) => callbacks.onPhaseChange?.(payload)
  sdk.onResultsChange = (payload) => callbacks.onResultsChange?.(payload)
  const engine: CloudflareEngineLike = {
    get results() {
      return sdk.results
    },
    get onFinish() {
      return callbacks.onFinish ?? (() => {})
    },
    set onFinish(callback) {
      callbacks.onFinish = callback
    },
    get onError() {
      return callbacks.onError ?? (() => {})
    },
    set onError(callback) {
      callbacks.onError = callback
    },
    get onPhaseChange() {
      return callbacks.onPhaseChange ?? (() => {})
    },
    set onPhaseChange(callback) {
      callbacks.onPhaseChange = callback
    },
    get onResultsChange() {
      return callbacks.onResultsChange ?? (() => {})
    },
    set onResultsChange(callback) {
      callbacks.onResultsChange = callback
    },
    pause: () => sdk.pause(),
    play: () => sdk.play(),
  }
  lastEngine = engine
  return engine
}

function assertScenario(value: string): P0GScenario {
  if (!scenarios.has(value as P0GScenario)) {
    throw new Error(`unknown P0-G scenario: ${value}`)
  }
  return value as P0GScenario
}

function start(scenarioValue: P0GScenario): P0GStartAck {
  const scenario = assertScenario(scenarioValue)
  if (completion) {
    return {
      started: false,
      scenario: activeScenario,
      reason: 'a P0-G run is already active',
    }
  }

  activeScenario = scenario
  progressEvents = []
  lastEngine = null
  originalFetch = globalThis.fetch
  provider = new CloudflareSpeedTestProvider({
    profile: CLOUDFLARE_POC_PROFILE,
    engineFactory: createEngine,
    downloadApiUrl: mockEndpoint('/__down', scenario),
    uploadApiUrl: mockEndpoint('/__up', scenario),
    perRequestTimeoutMs: 20_000,
    overallTimeoutMs: scenario === 'per_request_timeout' ? 120_000 : 90_000,
    onProgress: (event) => {
      progressEvents.push(event)
    },
  })

  const currentProvider = provider
  completion = currentProvider.start().then((result) => ({
    result,
    progressCountAtSettle: progressEvents.length,
    fetchRestored: globalThis.fetch === originalFetch,
    providerRunning: currentProvider.isRunning,
    scenario,
  }))
  return { started: true, scenario }
}

async function awaitResult(): Promise<P0GSettledOutcome> {
  if (!completion) throw new Error('no P0-G run is active')
  return completion
}

function cancel(): boolean {
  if (!provider) return false
  provider.cancel('P0-G local WebView2 cancellation test')
  return true
}

function dispose(): boolean {
  if (!provider) return false
  provider.dispose()
  return true
}

async function triggerLateCallbacks(): Promise<P0GLateCallbackOutcome> {
  const before = progressEvents.length
  const engine = lastEngine
  if (engine) {
    engine.onFinish(engine.results)
    engine.onError('P0-G late callback after timeout')
    engine.onPhaseChange({ measurementId: 99, measurement: { type: 'upload' } })
    engine.onResultsChange({ type: 'upload' })
  }
  await new Promise((resolve) => setTimeout(resolve, 50))
  const after = progressEvents.length
  return { before, after, ignored: before === after }
}

async function run(scenario: P0GScenario): Promise<P0GSettledOutcome> {
  const ack = start(scenario)
  if (!ack.started) throw new Error(ack.reason ?? 'P0-G run did not start')
  return awaitResult()
}

function resetRun(): void {
  provider?.dispose()
  provider = null
  completion = null
  activeScenario = null
}

async function postControlResult(id: number, payload: unknown): Promise<void> {
  if (!nativeFetch) throw new Error('native fetch is unavailable')
  const response = await nativeFetch(controlUrl(`__p0g/result?id=${encodeURIComponent(id)}`), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  })
  if (!response.ok) throw new Error(`post result returned ${response.status}`)
}

async function runControlled(scenario: P0GScenario) {
  if (scenario === 'cancel') {
    const ack = start(scenario)
    if (!ack.started) throw new Error(ack.reason ?? 'cancel run did not start')
    // Eight latency requests precede the first stalled 1 MiB download.
    await waitForInflight(scenario, 9)
    cancel()
    return awaitResult()
  }

  const ack = start(scenario)
  if (!ack.started) throw new Error(ack.reason ?? 'run did not start')
  const outcome = await awaitResult()
  if (scenario === 'per_request_timeout') {
    return {
      ...outcome,
      lateCallbacks: await triggerLateCallbacks(),
    }
  }
  return outcome
}

async function startControlLoop(): Promise<void> {
  if (controlLoopStarted) return
  controlLoopStarted = true
  while (true) {
    let command: { id?: number; scenario?: string }
    try {
      command = await controlFetchJson('__p0g/next')
    } catch {
      await sleep(200)
      continue
    }
    if (!command.scenario || !command.id) {
      await sleep(200)
      continue
    }
    if (!scenarios.has(command.scenario as P0GScenario)) continue
    const scenario = command.scenario as P0GScenario
    if (scenario === 'close') {
      start(scenario)
      return
    }
    try {
      const outcome = await runControlled(scenario)
      await postControlResult(command.id, { command, outcome })
    } catch (error) {
      await postControlResult(command.id, {
        command,
        error: error instanceof Error ? error.message : String(error),
      })
    } finally {
      resetRun()
    }
  }
}

function getState() {
  const activeProvider = provider
  return {
    scenario: activeScenario,
    running: activeProvider?.isRunning ?? false,
    progressCount: progressEvents.length,
    lastStage: progressEvents.at(-1)?.stage ?? null,
  }
}

export function installP0GHarness(): void {
  const target = globalThis as typeof globalThis & { __gospeedP0G?: P0GHarness }
  if (target.__gospeedP0G) return
  nativeFetch = globalThis.fetch?.bind(globalThis) ?? null
  target.__gospeedP0G = {
    version: 'p0g-1',
    start,
    run,
    awaitResult,
    cancel,
    dispose,
    triggerLateCallbacks,
    getState,
  }
  globalThis.addEventListener?.('beforeunload', () => {
    provider?.dispose()
  })
  if (import.meta.env.VITE_CLOUDFLARE_P0G_AUTORUN === 'true') {
    void startControlLoop()
  }
}
