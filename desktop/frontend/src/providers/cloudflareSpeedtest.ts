import SpeedTest from '@cloudflare/speedtest'
import type { ConfigOptions, MeasurementConfig } from '@cloudflare/speedtest'

export const CLOUDFLARE_PROVIDER_ID = 'cloudflare-speedtest'
export const CLOUDFLARE_SDK_VERSION = '1.14.1'

const MIB = 1024 * 1024

export type CloudflarePhase = 'latency' | 'download' | 'upload'
export type CloudflareResultStatus = 'completed' | 'failed' | 'cancelled' | 'busy'
export type CloudflareProgressStage =
  | 'starting'
  | 'phase'
  | 'progress'
  | 'completed'
  | 'failed'
  | 'cancelled'

export interface CloudflarePocProfile {
  measurements: MeasurementConfig[]
  downloadPayloadBytes: number
  uploadPayloadBytes: number
  totalPayloadBytes: number
}

export interface CloudflareProviderResult {
  provider: typeof CLOUDFLARE_PROVIDER_ID
  providerVersion: typeof CLOUDFLARE_SDK_VERSION
  status: CloudflareResultStatus
  startedAt: string
  completedAt: string
  downloadMbps: number | null
  uploadMbps: number | null
  latencyMs: number | null
  latencyKind: 'provider_ttfb'
  jitterMs: number | null
  configuredPayloadBytes: number
  completedPayloadBytes: number
  downloadedPayloadBytes: number
  uploadedPayloadBytes: number
  latencySamples: number
  durationMs: number | null
  error: string | null
  cancelled: boolean
  retryPolicy: 'sdk_429_max_3'
  cancellation: {
    newRequestsStopped: boolean
    abortRequested: boolean
    inFlightOutcome: 'abort_requested_unverified' | 'not_requested'
  } | null
}

export interface CloudflareProgress {
  provider: typeof CLOUDFLARE_PROVIDER_ID
  providerVersion: typeof CLOUDFLARE_SDK_VERSION
  phase: CloudflarePhase | null
  stage: CloudflareProgressStage
  elapsedMs: number
  message: string
  configuredPayloadBytes: number
  completedPayloadBytes: number
  downloadedPayloadBytes: number
  uploadedPayloadBytes: number
  latencySamples: number
  downloadMbps: number | null
  uploadMbps: number | null
  latencyMs: number | null
  jitterMs: number | null
}

export interface CloudflareBandwidthPointLike {
  bytes: number
  bps: number | null | undefined
}

export interface CloudflareResultsLike {
  getDownloadBandwidth(): number | undefined
  getDownloadBandwidthPoints(): CloudflareBandwidthPointLike[]
  getUploadBandwidth(): number | undefined
  getUploadBandwidthPoints(): CloudflareBandwidthPointLike[]
  getUnloadedLatency(): number | undefined
  getUnloadedLatencyPoints(): number[]
  getUnloadedJitter(): number | null | undefined
  getTotalDurationMs(): number | undefined
}

export interface CloudflareEngineLike {
  results: CloudflareResultsLike
  onFinish: (results: CloudflareResultsLike) => void
  onError: (message: string, status?: number) => void
  onPhaseChange: (payload: { measurementId: number; measurement: { type: string } }) => void
  onResultsChange: (payload: { type: string }) => void
  pause(): void
  play(): void
}

export interface CloudflareSpeedTestProviderOptions {
  profile?: CloudflarePocProfile
  engineFactory?: (config: ConfigOptions) => CloudflareEngineLike
  now?: () => number
  onProgress?: (progress: CloudflareProgress) => void
}

interface MetricSnapshot {
  downloadMbps: number | null
  uploadMbps: number | null
  latencyMs: number | null
  jitterMs: number | null
  completedPayloadBytes: number
  downloadedPayloadBytes: number
  uploadedPayloadBytes: number
  latencySamples: number
  durationMs: number | null
}

interface ActiveRun {
  generation: number
  engine: CloudflareEngineLike
  startedAtMs: number
  startedAt: string
  phase: CloudflarePhase | null
  settled: boolean
  resolve: (result: CloudflareProviderResult) => void
}

function measurementPayloadBytes(measurements: MeasurementConfig[], type: 'download' | 'upload'): number {
  return measurements.reduce((total, measurement) => {
    if (measurement.type !== type) return total
    return total + measurement.bytes * measurement.count
  }, 0)
}

export function summarizeCloudflarePayload(measurements: MeasurementConfig[]): {
  downloadPayloadBytes: number
  uploadPayloadBytes: number
  totalPayloadBytes: number
} {
  const downloadPayloadBytes = measurementPayloadBytes(measurements, 'download')
  const uploadPayloadBytes = measurementPayloadBytes(measurements, 'upload')
  return {
    downloadPayloadBytes,
    uploadPayloadBytes,
    totalPayloadBytes: downloadPayloadBytes + uploadPayloadBytes,
  }
}

const pocMeasurements: MeasurementConfig[] = [
  { type: 'latency', numPackets: 8 },
  { type: 'download', bytes: 1 * MIB, count: 2, bypassMinDuration: true },
  { type: 'download', bytes: 2 * MIB, count: 3, bypassMinDuration: true },
  { type: 'download', bytes: 4 * MIB, count: 4, bypassMinDuration: true },
  { type: 'upload', bytes: 1 * MIB, count: 2, bypassMinDuration: true },
  { type: 'upload', bytes: 2 * MIB, count: 3, bypassMinDuration: true },
  { type: 'upload', bytes: 4 * MIB, count: 2, bypassMinDuration: true },
]

export const CLOUDFLARE_POC_PROFILE: CloudflarePocProfile = Object.freeze({
  measurements: pocMeasurements.map((measurement) => ({ ...measurement })),
  ...summarizeCloudflarePayload(pocMeasurements),
})

function createRealEngine(config: ConfigOptions): CloudflareEngineLike {
  return new SpeedTest(config) as unknown as CloudflareEngineLike
}

function finiteNumber(value: number | null | undefined): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

function positiveMbps(bitsPerSecond: number | undefined, hasPoints: boolean): number | null {
  const finite = finiteNumber(bitsPerSecond)
  if (!hasPoints || finite === null || finite <= 0) return null
  return finite / 1_000_000
}

function messageOf(error: unknown): string {
  if (error instanceof Error) return error.message
  return String(error)
}

function normalizePhase(type: string): CloudflarePhase | null {
  if (type === 'latency' || type === 'download' || type === 'upload') return type
  return null
}

function readPoints(result: CloudflareResultsLike, direction: 'download' | 'upload'): CloudflareBandwidthPointLike[] {
  try {
    return direction === 'download' ? result.getDownloadBandwidthPoints() : result.getUploadBandwidthPoints()
  } catch {
    return []
  }
}

function readMetrics(run: ActiveRun, now: () => number): MetricSnapshot {
  const results = run.engine.results
  const downloadPoints = readPoints(results, 'download')
  const uploadPoints = readPoints(results, 'upload')
  let latencyPoints: number[] = []
  let latency: number | undefined
  let jitter: number | null | undefined
  let downloadBps: number | undefined
  let uploadBps: number | undefined
  let reportedDuration: number | undefined

  try {
    latencyPoints = results.getUnloadedLatencyPoints()
    latency = results.getUnloadedLatency()
    jitter = results.getUnloadedJitter()
    downloadBps = results.getDownloadBandwidth()
    uploadBps = results.getUploadBandwidth()
    reportedDuration = results.getTotalDurationMs()
  } catch {
    // Partial SDK results can be temporarily incomplete while a phase is running.
  }

  const downloadedPayloadBytes = downloadPoints.reduce((total, point) => total + point.bytes, 0)
  const uploadedPayloadBytes = uploadPoints.reduce((total, point) => total + point.bytes, 0)
  const measuredLatency = latencyPoints.length > 0 ? finiteNumber(latency) : null
  const jitterValue = latencyPoints.length > 1 ? finiteNumber(jitter) : null
  const durationMs = finiteNumber(reportedDuration) ?? Math.max(0, now() - run.startedAtMs)

  return {
    downloadMbps: positiveMbps(downloadBps, downloadPoints.length > 0),
    uploadMbps: positiveMbps(uploadBps, uploadPoints.length > 0),
    latencyMs: measuredLatency,
    jitterMs: jitterValue,
    completedPayloadBytes: downloadedPayloadBytes + uploadedPayloadBytes,
    downloadedPayloadBytes,
    uploadedPayloadBytes,
    latencySamples: latencyPoints.length,
    durationMs,
  }
}

export class CloudflareSpeedTestProvider {
  readonly #profile: CloudflarePocProfile
  readonly #engineFactory: (config: ConfigOptions) => CloudflareEngineLike
  readonly #now: () => number
  readonly #onProgress: (progress: CloudflareProgress) => void

  #active: ActiveRun | null = null
  #generation = 0
  #disposed = false

  constructor(options: CloudflareSpeedTestProviderOptions = {}) {
    this.#profile = options.profile ?? CLOUDFLARE_POC_PROFILE
    this.#engineFactory = options.engineFactory ?? createRealEngine
    this.#now = options.now ?? (() => performance.now())
    this.#onProgress = options.onProgress ?? (() => {})
  }

  get isRunning(): boolean {
    return this.#active !== null
  }

  start(): Promise<CloudflareProviderResult> {
    if (this.#disposed) {
      return Promise.resolve(this.#emptyResult('failed', 'Cloudflare PoC Provider 已释放'))
    }
    if (this.#active) {
      return Promise.resolve(this.#emptyResult('busy', 'Cloudflare 测速已在进行中'))
    }

    const config: ConfigOptions = {
      autoStart: false,
      logMeasurementApiUrl: null,
      logAimApiUrl: null,
      includeCredentials: false,
      measureDownloadLoadedLatency: false,
      measureUploadLoadedLatency: false,
      measurements: this.#profile.measurements.map((measurement) => ({ ...measurement })),
    }

    let engine: CloudflareEngineLike
    try {
      engine = this.#engineFactory(config)
    } catch (error) {
      return Promise.resolve(this.#emptyResult('failed', `Cloudflare SDK 初始化失败：${messageOf(error)}`))
    }

    const startedAtMs = this.#now()
    const run: ActiveRun = {
      generation: ++this.#generation,
      engine,
      startedAtMs,
      startedAt: new Date().toISOString(),
      phase: null,
      settled: false,
      resolve: () => {},
    }
    const completion = new Promise<CloudflareProviderResult>((resolve) => {
      run.resolve = resolve
    })
    this.#active = run

    engine.onPhaseChange = ({ measurement }) => {
      if (!this.#isActive(run)) return
      run.phase = normalizePhase(measurement.type)
      this.#emit(run, 'phase', `${run.phase ?? measurement.type} 阶段`)
    }
    engine.onResultsChange = () => {
      if (!this.#isActive(run)) return
      this.#emit(run, 'progress', '收到一个已完成的测量请求')
    }
    engine.onFinish = () => {
      if (!this.#isActive(run)) return
      this.#settle(run, 'completed', null)
    }
    engine.onError = (message, status) => {
      if (!this.#isActive(run)) return
      const suffix = typeof status === 'number' ? `（HTTP ${status}）` : ''
      this.#settle(run, 'failed', `${message}${suffix}`)
    }

    this.#emit(run, 'starting', 'Cloudflare Speedtest PoC 已启动')
    try {
      engine.play()
    } catch (error) {
      this.#settle(run, 'failed', `Cloudflare Speedtest 启动失败：${messageOf(error)}`)
    }
    return completion
  }

  cancel(reason = '用户取消测速'): void {
    const run = this.#active
    if (!run) return

    this.#active = null
    this.#generation++
    let abortRequested = false
    try {
      run.engine.pause()
      abortRequested = true
    } catch {
      // The result records that the SDK did not accept the abort request.
    }
    this.#settle(run, 'cancelled', reason, {
      newRequestsStopped: abortRequested,
      abortRequested,
      inFlightOutcome: abortRequested ? 'abort_requested_unverified' : 'not_requested',
    })
  }

  dispose(): void {
    if (this.#active) this.cancel('GUI 已关闭，取消 Cloudflare PoC')
    this.#disposed = true
  }

  #isActive(run: ActiveRun): boolean {
    return !this.#disposed && !run.settled && this.#active === run && this.#generation === run.generation
  }

  #emit(run: ActiveRun, stage: CloudflareProgressStage, message: string): void {
    const metrics = readMetrics(run, this.#now)
    this.#onProgress({
      provider: CLOUDFLARE_PROVIDER_ID,
      providerVersion: CLOUDFLARE_SDK_VERSION,
      phase: run.phase,
      stage,
      elapsedMs: metrics.durationMs ?? Math.max(0, this.#now() - run.startedAtMs),
      message,
      configuredPayloadBytes: this.#profile.totalPayloadBytes,
      completedPayloadBytes: metrics.completedPayloadBytes,
      downloadedPayloadBytes: metrics.downloadedPayloadBytes,
      uploadedPayloadBytes: metrics.uploadedPayloadBytes,
      latencySamples: metrics.latencySamples,
      downloadMbps: metrics.downloadMbps,
      uploadMbps: metrics.uploadMbps,
      latencyMs: metrics.latencyMs,
      jitterMs: metrics.jitterMs,
    })
  }

  #settle(
    run: ActiveRun,
    status: Exclude<CloudflareResultStatus, 'busy'>,
    error: string | null,
    cancellation: CloudflareProviderResult['cancellation'] = null,
  ): void {
    if (run.settled) return
    run.settled = true
    if (this.#active === run) this.#active = null
    this.#generation++

    const metrics = readMetrics(run, this.#now)
    const result: CloudflareProviderResult = {
      provider: CLOUDFLARE_PROVIDER_ID,
      providerVersion: CLOUDFLARE_SDK_VERSION,
      status,
      startedAt: run.startedAt,
      completedAt: new Date().toISOString(),
      downloadMbps: metrics.downloadMbps,
      uploadMbps: metrics.uploadMbps,
      latencyMs: metrics.latencyMs,
      latencyKind: 'provider_ttfb',
      jitterMs: metrics.jitterMs,
      configuredPayloadBytes: this.#profile.totalPayloadBytes,
      completedPayloadBytes: metrics.completedPayloadBytes,
      downloadedPayloadBytes: metrics.downloadedPayloadBytes,
      uploadedPayloadBytes: metrics.uploadedPayloadBytes,
      latencySamples: metrics.latencySamples,
      durationMs: metrics.durationMs,
      error,
      cancelled: status === 'cancelled',
      retryPolicy: 'sdk_429_max_3',
      cancellation,
    }
    this.#onProgress({
      provider: result.provider,
      providerVersion: result.providerVersion,
      phase: run.phase,
      stage: status === 'completed' ? 'completed' : status === 'cancelled' ? 'cancelled' : 'failed',
      elapsedMs: metrics.durationMs ?? Math.max(0, this.#now() - run.startedAtMs),
      message: error ?? (status === 'completed' ? 'Cloudflare Speedtest 完成' : 'Cloudflare Speedtest 已取消'),
      configuredPayloadBytes: result.configuredPayloadBytes,
      completedPayloadBytes: result.completedPayloadBytes,
      downloadedPayloadBytes: result.downloadedPayloadBytes,
      uploadedPayloadBytes: result.uploadedPayloadBytes,
      latencySamples: result.latencySamples,
      downloadMbps: result.downloadMbps,
      uploadMbps: result.uploadMbps,
      latencyMs: result.latencyMs,
      jitterMs: result.jitterMs,
    })
    run.resolve(result)
  }

  #emptyResult(status: CloudflareResultStatus, error: string): CloudflareProviderResult {
    const now = new Date().toISOString()
    return {
      provider: CLOUDFLARE_PROVIDER_ID,
      providerVersion: CLOUDFLARE_SDK_VERSION,
      status,
      startedAt: now,
      completedAt: now,
      downloadMbps: null,
      uploadMbps: null,
      latencyMs: null,
      latencyKind: 'provider_ttfb',
      jitterMs: null,
      configuredPayloadBytes: this.#profile.totalPayloadBytes,
      completedPayloadBytes: 0,
      downloadedPayloadBytes: 0,
      uploadedPayloadBytes: 0,
      latencySamples: 0,
      durationMs: 0,
      error,
      cancelled: false,
      retryPolicy: 'sdk_429_max_3',
      cancellation: null,
    }
  }
}
