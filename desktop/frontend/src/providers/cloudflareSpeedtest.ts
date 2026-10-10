import SpeedTest from '@cloudflare/speedtest'
import type { ConfigOptions, MeasurementConfig } from '@cloudflare/speedtest'
import {
  CloudflareFetchTelemetry,
  type CloudflareDiagnostics,
  type CloudflareQualityAssessment,
  type CloudflareRawBandwidthPoint,
  type CloudflareRawResults,
  type CloudflareTelemetryLiveSummary,
  type CloudflareTelemetrySessionLike,
} from './cloudflareTelemetry'

export const CLOUDFLARE_PROVIDER_ID = 'cloudflare-speedtest'
export const CLOUDFLARE_SDK_VERSION = '1.14.1'

const MIB = 1024 * 1024

export type CloudflarePhase = 'latency' | 'download' | 'upload'
export type CloudflareResultStatus = 'completed' | 'failed' | 'cancelled' | 'timeout' | 'busy'
export type CloudflareProgressStage =
  | 'starting'
  | 'phase'
  | 'progress'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'timeout'

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
  timedOut: boolean
  retryPolicy: 'sdk_429_max_3'
  diagnostics: CloudflareDiagnostics | null
  quality: CloudflareQualityAssessment | null
  timeoutPolicy: {
    perRequestMs: number
    overallMs: number
    reason: 'per_request' | 'overall' | null
  }
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
  phaseElapsedMs: number
  telemetry: CloudflareTelemetryLiveSummary
  quality: CloudflareQualityAssessment | null
}

export interface CloudflareBandwidthPointLike {
  bytes: number
  bps: number | null | undefined
  duration?: number
  ping?: number
  measTime?: Date | string
  serverTime?: number
  transferSize?: number
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
  telemetryFactory?: () => CloudflareTelemetrySessionLike
  downloadApiUrl?: string
  uploadApiUrl?: string
  perRequestTimeoutMs?: number
  overallTimeoutMs?: number
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
  completedMeasurementPoints: number
  durationMs: number | null
}

interface ActiveRun {
  generation: number
  engine: CloudflareEngineLike
  startedAtMs: number
  startedAt: string
  phase: CloudflarePhase | null
  phaseStartedAtMs: number | null
  telemetry: CloudflareTelemetrySessionLike
  overallTimeout: ReturnType<typeof setTimeout> | null
  timeoutReason: 'per_request' | 'overall' | null
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

export const CLOUDFLARE_TIMEOUT_DEFAULTS = Object.freeze({
  perRequestMs: 20_000,
  overallMs: 90_000,
})

export function expectedMeasurementPoints(profile: CloudflarePocProfile): number {
  return profile.measurements.reduce((total, measurement) => {
    if (measurement.type === 'latency') return total + measurement.numPackets
    if (measurement.type === 'download' || measurement.type === 'upload') {
      return total + measurement.count
    }
    return total
  }, 0)
}

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

function normalizeBandwidthPoint(point: CloudflareBandwidthPointLike): CloudflareRawBandwidthPoint {
  const measTime =
    point.measTime instanceof Date
      ? point.measTime.toISOString()
      : typeof point.measTime === 'string'
        ? point.measTime
        : null
  return {
    bytes: point.bytes,
    bps: finiteNumber(point.bps),
    durationMs: finiteNumber(point.duration),
    pingMs: finiteNumber(point.ping),
    measTime,
    serverTimeMs:
      typeof point.serverTime === 'number' && point.serverTime >= 0
        ? point.serverTime
        : null,
    transferSize: finiteNumber(point.transferSize),
  }
}

function readRawResults(result: CloudflareResultsLike): CloudflareRawResults {
  let unloadedLatencyPoints: number[] = []
  let totalDurationMs: number | undefined
  try {
    unloadedLatencyPoints = result.getUnloadedLatencyPoints()
    totalDurationMs = result.getTotalDurationMs()
  } catch {
    // Raw SDK data may be incomplete after an error or cancellation.
  }
  return {
    unloadedLatencyPoints,
    downloadBandwidthPoints: readPoints(result, 'download').map(normalizeBandwidthPoint),
    uploadBandwidthPoints: readPoints(result, 'upload').map(normalizeBandwidthPoint),
    totalDurationMs: finiteNumber(totalDurationMs),
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
  const completedMeasurementPoints =
    latencyPoints.length + downloadPoints.length + uploadPoints.length

  return {
    downloadMbps: positiveMbps(downloadBps, downloadPoints.length > 0),
    uploadMbps: positiveMbps(uploadBps, uploadPoints.length > 0),
    latencyMs: measuredLatency,
    jitterMs: jitterValue,
    completedPayloadBytes: downloadedPayloadBytes + uploadedPayloadBytes,
    downloadedPayloadBytes,
    uploadedPayloadBytes,
    latencySamples: latencyPoints.length,
    completedMeasurementPoints,
    durationMs,
  }
}

export class CloudflareSpeedTestProvider {
  readonly #profile: CloudflarePocProfile
  readonly #engineFactory: (config: ConfigOptions) => CloudflareEngineLike
  readonly #telemetryFactory: () => CloudflareTelemetrySessionLike
  readonly #downloadApiUrl: string | undefined
  readonly #uploadApiUrl: string | undefined
  readonly #perRequestTimeoutMs: number
  readonly #overallTimeoutMs: number
  readonly #now: () => number
  readonly #onProgress: (progress: CloudflareProgress) => void

  #active: ActiveRun | null = null
  #generation = 0
  #disposed = false

  constructor(options: CloudflareSpeedTestProviderOptions = {}) {
    this.#profile = options.profile ?? CLOUDFLARE_POC_PROFILE
    this.#engineFactory = options.engineFactory ?? createRealEngine
    this.#telemetryFactory = options.telemetryFactory ?? (() => new CloudflareFetchTelemetry())
    this.#downloadApiUrl = options.downloadApiUrl?.trim() || undefined
    this.#uploadApiUrl = options.uploadApiUrl?.trim() || undefined
    this.#perRequestTimeoutMs = Math.max(
      0,
      options.perRequestTimeoutMs ?? CLOUDFLARE_TIMEOUT_DEFAULTS.perRequestMs,
    )
    this.#overallTimeoutMs = Math.max(
      0,
      options.overallTimeoutMs ?? CLOUDFLARE_TIMEOUT_DEFAULTS.overallMs,
    )
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
      bandwidthAbortRequestDuration: this.#perRequestTimeoutMs,
      ...(this.#downloadApiUrl ? { downloadApiUrl: this.#downloadApiUrl } : {}),
      ...(this.#uploadApiUrl ? { uploadApiUrl: this.#uploadApiUrl } : {}),
      measurements: this.#profile.measurements.map((measurement) => ({ ...measurement })),
    }

    let engine: CloudflareEngineLike
    try {
      engine = this.#engineFactory(config)
    } catch (error) {
      return Promise.resolve(this.#emptyResult('failed', `Cloudflare SDK 初始化失败：${messageOf(error)}`))
    }

    let telemetry: CloudflareTelemetrySessionLike
    try {
      telemetry = this.#telemetryFactory()
      telemetry.install()
    } catch (error) {
      return Promise.resolve(this.#emptyResult('failed', `Cloudflare 遥测初始化失败：${messageOf(error)}`))
    }

    const startedAtMs = this.#now()
    const run: ActiveRun = {
      generation: ++this.#generation,
      engine,
      startedAtMs,
      startedAt: new Date().toISOString(),
      phase: null,
      phaseStartedAtMs: null,
      telemetry,
      overallTimeout: null,
      timeoutReason: null,
      settled: false,
      resolve: () => {},
    }
    const completion = new Promise<CloudflareProviderResult>((resolve) => {
      run.resolve = resolve
    })
    this.#active = run

    engine.onPhaseChange = ({ measurementId, measurement }) => {
      if (!this.#isActive(run)) return
      run.phase = normalizePhase(measurement.type)
      run.phaseStartedAtMs = this.#now()
      run.telemetry.beginPhase(measurementId, measurement.type)
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
      const perRequestTimeout = /bandwidthAbortRequestDuration/i.test(message)
      this.#settle(
        run,
        perRequestTimeout ? 'timeout' : 'failed',
        `${message}${suffix}`,
        null,
        perRequestTimeout ? 'per_request' : null,
      )
    }

    if (this.#overallTimeoutMs > 0) {
      run.overallTimeout = setTimeout(
        () => this.#handleOverallTimeout(run),
        this.#overallTimeoutMs,
      )
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

  #handleOverallTimeout(run: ActiveRun): void {
    if (!this.#isActive(run)) return
    this.#active = null
    this.#generation++
    let abortRequested = false
    try {
      run.engine.pause()
      abortRequested = true
    } catch {
      // The timeout result records that the SDK did not accept the abort request.
    }
    this.#settle(
      run,
      'timeout',
      `Cloudflare Speedtest 超过整次运行上限 ${this.#overallTimeoutMs} ms`,
      abortRequested
        ? {
            newRequestsStopped: true,
            abortRequested: true,
            inFlightOutcome: 'abort_requested_unverified',
          }
        : null,
      'overall',
    )
  }

  #isActive(run: ActiveRun): boolean {
    return !this.#disposed && !run.settled && this.#active === run && this.#generation === run.generation
  }

  #emit(run: ActiveRun, stage: CloudflareProgressStage, message: string): void {
    const metrics = readMetrics(run, this.#now)
    const telemetry = run.telemetry.liveSummary(metrics.completedMeasurementPoints)
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
      phaseElapsedMs:
        run.phaseStartedAtMs === null
          ? 0
          : Math.max(0, this.#now() - run.phaseStartedAtMs),
      telemetry,
      quality: null,
    })
  }

  #settle(
    run: ActiveRun,
    status: Exclude<CloudflareResultStatus, 'busy'>,
    error: string | null,
    cancellation: CloudflareProviderResult['cancellation'] = null,
    timeoutReason: ActiveRun['timeoutReason'] = null,
  ): void {
    if (run.settled) return
    run.settled = true
    if (run.overallTimeout !== null) {
      clearTimeout(run.overallTimeout)
      run.overallTimeout = null
    }
    if (this.#active === run) this.#active = null
    this.#generation++

    run.timeoutReason = timeoutReason
    const metrics = readMetrics(run, this.#now)
    const rawResults = readRawResults(run.engine.results)
    let diagnostics: CloudflareDiagnostics | null = null
    try {
      run.telemetry.endPhase()
      diagnostics = run.telemetry.snapshot({
        status,
        error,
        configuredPayloadBytes: this.#profile.totalPayloadBytes,
        expectedMeasurementPoints: expectedMeasurementPoints(this.#profile),
        perRequestTimeoutMs: this.#perRequestTimeoutMs,
        overallTimeoutMs: this.#overallTimeoutMs,
        rawResults,
      })
    } catch {
      diagnostics = null
    } finally {
      run.telemetry.dispose()
    }
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
      timedOut: status === 'timeout',
      retryPolicy: 'sdk_429_max_3',
      diagnostics,
      quality: diagnostics?.quality ?? null,
      timeoutPolicy: {
        perRequestMs: this.#perRequestTimeoutMs,
        overallMs: this.#overallTimeoutMs,
        reason: timeoutReason,
      },
      cancellation,
    }
    const telemetry =
      diagnostics?.summary ?? run.telemetry.liveSummary(metrics.completedMeasurementPoints)
    this.#onProgress({
      provider: result.provider,
      providerVersion: result.providerVersion,
      phase: run.phase,
      stage:
        status === 'completed'
          ? 'completed'
          : status === 'cancelled'
            ? 'cancelled'
            : status === 'timeout'
              ? 'timeout'
              : 'failed',
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
      phaseElapsedMs:
        run.phaseStartedAtMs === null
          ? 0
          : Math.max(0, this.#now() - run.phaseStartedAtMs),
      telemetry,
      quality: result.quality,
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
      timedOut: false,
      retryPolicy: 'sdk_429_max_3',
      diagnostics: null,
      quality: null,
      timeoutPolicy: {
        perRequestMs: this.#perRequestTimeoutMs,
        overallMs: this.#overallTimeoutMs,
        reason: null,
      },
      cancellation: null,
    }
  }
}
