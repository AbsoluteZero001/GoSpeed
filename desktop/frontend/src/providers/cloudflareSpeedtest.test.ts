import { describe, expect, it, vi } from 'vitest'
import type { ConfigOptions } from '@cloudflare/speedtest'
import {
  CLOUDFLARE_POC_PROFILE,
  CLOUDFLARE_PROVIDER_ID,
  CLOUDFLARE_SDK_VERSION,
  CloudflareSpeedTestProvider,
  type CloudflareBandwidthPointLike,
  type CloudflareEngineLike,
  type CloudflareProgress,
  type CloudflareResultsLike,
} from './cloudflareSpeedtest'

const MIB = 1024 * 1024

interface MockMetrics {
  downloadBps?: number
  uploadBps?: number
  downloadPoints?: CloudflareBandwidthPointLike[]
  uploadPoints?: CloudflareBandwidthPointLike[]
  latencyPoints?: number[]
  latency?: number
  jitter?: number | null
  durationMs?: number
}

function createMockEngine(initial: MockMetrics = {}) {
  let metrics: MockMetrics = initial
  const results: CloudflareResultsLike = {
    getDownloadBandwidth: vi.fn(() => metrics.downloadBps),
    getDownloadBandwidthPoints: vi.fn(() => metrics.downloadPoints ?? []),
    getUploadBandwidth: vi.fn(() => metrics.uploadBps),
    getUploadBandwidthPoints: vi.fn(() => metrics.uploadPoints ?? []),
    getUnloadedLatency: vi.fn(() => metrics.latency),
    getUnloadedLatencyPoints: vi.fn(() => metrics.latencyPoints ?? []),
    getUnloadedJitter: vi.fn(() => metrics.jitter),
    getTotalDurationMs: vi.fn(() => metrics.durationMs),
  }
  const engine: CloudflareEngineLike = {
    results,
    onFinish: () => {},
    onError: () => {},
    onPhaseChange: () => {},
    onResultsChange: () => {},
    pause: vi.fn(),
    play: vi.fn(),
  }

  return {
    engine,
    setMetrics(next: MockMetrics) {
      metrics = { ...metrics, ...next }
    },
    emitPhase(type: string) {
      engine.onPhaseChange({ measurementId: 0, measurement: { type } })
    },
    emitResults() {
      engine.onResultsChange({ type: 'download' })
    },
    finish() {
      engine.onFinish(results)
    },
    fail(message: string, status?: number) {
      engine.onError(message, status)
    },
  }
}

function createProvider(
  engines: ReturnType<typeof createMockEngine>[],
  configs: ConfigOptions[],
  onProgress: (progress: CloudflareProgress) => void,
) {
  let index = 0
  const provider = new CloudflareSpeedTestProvider({
    engineFactory: (config) => {
      configs.push(config)
      const mock = engines[index]
      index += 1
      if (!mock) throw new Error('missing mock engine')
      return mock.engine
    },
    now: () => 10_000,
    onProgress,
  })
  return provider
}

describe('Cloudflare PoC profile', () => {
  it('uses the planned 24 MiB download and 16 MiB upload payload budget', () => {
    expect(CLOUDFLARE_POC_PROFILE.downloadPayloadBytes).toBe(24 * MIB)
    expect(CLOUDFLARE_POC_PROFILE.uploadPayloadBytes).toBe(16 * MIB)
    expect(CLOUDFLARE_POC_PROFILE.totalPayloadBytes).toBe(40 * MIB)
  })

  it('contains only pausable latency, download and upload phases', () => {
    expect(CLOUDFLARE_POC_PROFILE.measurements.map((measurement) => measurement.type)).toEqual([
      'latency',
      'download',
      'download',
      'download',
      'upload',
      'upload',
      'upload',
    ])
  })
})

describe('CloudflareSpeedTestProvider', () => {
  it('does not create or start an SDK engine during construction', () => {
    const factory = vi.fn()
    const provider = new CloudflareSpeedTestProvider({ engineFactory: factory })

    expect(provider.isRunning).toBe(false)
    expect(factory).not.toHaveBeenCalled()
  })

  it('starts the official SDK with logging and loaded latency disabled', async () => {
    const mock = createMockEngine()
    const configs: ConfigOptions[] = []
    const progress: CloudflareProgress[] = []
    const provider = createProvider([mock], configs, (event) => progress.push(event))

    const completion = provider.start()
    expect(mock.engine.play).toHaveBeenCalledTimes(1)
    expect(configs[0]).toMatchObject({
      autoStart: false,
      logMeasurementApiUrl: null,
      logAimApiUrl: null,
      includeCredentials: false,
      measureDownloadLoadedLatency: false,
      measureUploadLoadedLatency: false,
    })
    expect(configs[0].measurements).toEqual(CLOUDFLARE_POC_PROFILE.measurements)

    mock.setMetrics({
      downloadBps: 80_000_000,
      uploadBps: 20_000_000,
      downloadPoints: [{ bytes: 24 * MIB, bps: 80_000_000 }],
      uploadPoints: [{ bytes: 16 * MIB, bps: 20_000_000 }],
      latencyPoints: [18, 22, 20],
      latency: 20,
      jitter: 2,
      durationMs: 14_000,
    })
    mock.emitPhase('download')
    mock.emitResults()
    mock.finish()

    const result = await completion
    expect(result).toMatchObject({
      provider: CLOUDFLARE_PROVIDER_ID,
      providerVersion: CLOUDFLARE_SDK_VERSION,
      status: 'completed',
      downloadMbps: 80,
      uploadMbps: 20,
      latencyMs: 20,
      latencyKind: 'provider_ttfb',
      jitterMs: 2,
      configuredPayloadBytes: 40 * MIB,
      completedPayloadBytes: 40 * MIB,
      cancelled: false,
      error: null,
    })
    expect(progress.some((event) => event.phase === 'download')).toBe(true)
    expect(progress.at(-1)?.stage).toBe('completed')
  })

  it('cancels the active engine and ignores late callbacks', async () => {
    const mock = createMockEngine()
    const configs: ConfigOptions[] = []
    const progress: CloudflareProgress[] = []
    const provider = createProvider([mock], configs, (event) => progress.push(event))

    const completion = provider.start()
    mock.emitPhase('upload')
    provider.cancel('test cancel')
    const result = await completion
    const progressCount = progress.length

    expect(mock.engine.pause).toHaveBeenCalledTimes(1)
    expect(result.status).toBe('cancelled')
    expect(result.cancelled).toBe(true)
    expect(result.cancellation).toEqual({
      newRequestsStopped: true,
      abortRequested: true,
      inFlightOutcome: 'abort_requested_unverified',
    })
    expect(progress.at(-1)?.stage).toBe('cancelled')

    mock.setMetrics({ downloadBps: 999_000_000, downloadPoints: [{ bytes: 24 * MIB, bps: 999_000_000 }] })
    mock.emitResults()
    mock.finish()
    await Promise.resolve()
    expect(progress).toHaveLength(progressCount)
  })

  it('rejects a second GO without creating another SDK engine', async () => {
    const first = createMockEngine()
    const configs: ConfigOptions[] = []
    const provider = createProvider([first], configs, () => {})

    const firstCompletion = provider.start()
    const second = await provider.start()

    expect(second.status).toBe('busy')
    expect(configs).toHaveLength(1)
    expect(first.engine.play).toHaveBeenCalledTimes(1)

    provider.cancel()
    await firstCompletion
  })

  it('allows a new run after cancellation without old data pollution', async () => {
    const first = createMockEngine()
    const second = createMockEngine()
    const configs: ConfigOptions[] = []
    const progress: CloudflareProgress[] = []
    const provider = createProvider([first, second], configs, (event) => progress.push(event))

    const firstCompletion = provider.start()
    provider.cancel()
    await firstCompletion

    const secondCompletion = provider.start()
    first.setMetrics({ downloadBps: 999_000_000 })
    first.finish()
    await Promise.resolve()

    second.setMetrics({
      downloadBps: 50_000_000,
      uploadBps: 10_000_000,
      downloadPoints: [{ bytes: 24 * MIB, bps: 50_000_000 }],
      uploadPoints: [{ bytes: 16 * MIB, bps: 10_000_000 }],
      latencyPoints: [30, 34],
      latency: 32,
      jitter: 4,
      durationMs: 20_000,
    })
    second.finish()
    const result = await secondCompletion

    expect(configs).toHaveLength(2)
    expect(result.status).toBe('completed')
    expect(result.downloadMbps).toBe(50)
    expect(progress.at(-1)?.downloadMbps).toBe(50)
  })

  it('recovers after an SDK error and accepts a later run', async () => {
    const first = createMockEngine()
    const second = createMockEngine()
    const configs: ConfigOptions[] = []
    const provider = createProvider([first, second], configs, () => {})

    const failed = provider.start()
    first.fail('HTTP request failed', 403)
    expect((await failed).status).toBe('failed')

    const next = provider.start()
    expect(second.engine.play).toHaveBeenCalledTimes(1)
    provider.cancel()
    expect((await next).status).toBe('cancelled')
    expect(configs).toHaveLength(2)
  })

  it('cancels the active run when the GUI is disposed', async () => {
    const mock = createMockEngine()
    const configs: ConfigOptions[] = []
    const provider = createProvider([mock], configs, () => {})

    const completion = provider.start()
    provider.dispose()
    const result = await completion
    const rejected = await provider.start()

    expect(mock.engine.pause).toHaveBeenCalledTimes(1)
    expect(result.status).toBe('cancelled')
    expect(rejected.status).toBe('failed')
    expect(rejected.error).toContain('已释放')
  })
})
