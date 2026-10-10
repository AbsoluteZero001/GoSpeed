export const CLOUDFLARE_DIAGNOSTICS_SCHEMA = 'gospeed-cf-poc-diagnostics-v1'

export type CloudflareEndpointClass =
  | 'latency'
  | 'download'
  | 'upload'
  | 'results'
  | 'turn'
  | 'other'

export type CloudflareHostClass = 'cloudflare-speedtest' | 'local-mock' | 'external'

export interface CloudflareRequestTelemetry {
  requestId: string
  endpointClass: CloudflareEndpointClass
  hostClass: CloudflareHostClass
  method: string
  queryKeys: string[]
  bodyType: string
  startedAt: string
  endedAt: string
  startOffsetMs: number
  endOffsetMs: number
  durationMs: number
  gapSincePreviousEndMs: number | null
  httpStatus: number | null
  retryAfter: string | null
  failed: boolean
  failureReason: string | null
  aborted: boolean
  is429: boolean
  is5xx: boolean
  after429: boolean
  retryCandidate: boolean
  responseContentLengthBytes: number | null
}

export interface CloudflarePhaseTelemetry {
  measurementId: number
  type: string
  startedAt: string
  endedAt: string
  startOffsetMs: number
  endOffsetMs: number
  durationMs: number
  idleBeforeMs: number
}

export interface CloudflareRawBandwidthPoint {
  bytes: number
  bps: number | null
  durationMs: number | null
  pingMs: number | null
  measTime: string | null
  serverTimeMs: number | null
  transferSize: number | null
}

export interface CloudflareRawResults {
  unloadedLatencyPoints: number[]
  downloadBandwidthPoints: CloudflareRawBandwidthPoint[]
  uploadBandwidthPoints: CloudflareRawBandwidthPoint[]
  totalDurationMs: number | null
}

export interface CloudflareTelemetryLiveSummary {
  requestCount: number
  failedRequestCount: number
  http429Count: number
  http5xxCount: number
  maxRequestDurationMs: number | null
  observableRetryCandidates: number
  completedMeasurementPoints: number
}

export interface CloudflareDiagnosticsSummary extends CloudflareTelemetryLiveSummary {
  abortedRequestCount: number
  statusCounts: Record<string, number>
  observableResponseContentLengthBytes: number
  latencyRequestCount: number
  downloadRequestCount: number
  uploadRequestCount: number
  otherRequestCount: number
}

export interface CloudflareDiagnostics {
  schema: typeof CLOUDFLARE_DIAGNOSTICS_SCHEMA
  provider: 'cloudflare-speedtest'
  providerVersion: '1.14.1'
  status: 'completed' | 'failed' | 'cancelled' | 'timeout'
  error: string | null
  startedAt: string
  completedAt: string
  durationMs: number
  configuredPayloadBytes: number
  requests: CloudflareRequestTelemetry[]
  phases: CloudflarePhaseTelemetry[]
  rawResults: CloudflareRawResults
  summary: CloudflareDiagnosticsSummary
  quality: CloudflareQualityAssessment
  timeoutPolicy: {
    perRequestMs: number
    overallMs: number
  }
  measurementSemantics: {
    latency: 'provider_ttfb'
    jitter: 'mean_absolute_difference_consecutive_latency_samples'
    downloadBandwidth: 'sdk_configured_percentile_of_measured_requests'
    uploadBandwidth: 'sdk_configured_percentile_of_measured_requests'
    bandwidthPercentile: 0.9
    bandwidthMinRequestDurationMs: 10
    note: string
  }
  privacy: {
    recordsFullUrl: false
    recordsPublicIp: false
    recordsHeaders: false
    recordsAuthorization: false
    recordsCookies: false
    recordsRequestBody: false
    responseBodyRead: false
    note: string
  }
}

export interface CloudflareTelemetrySnapshotInput {
  status: 'completed' | 'failed' | 'cancelled' | 'timeout'
  error: string | null
  configuredPayloadBytes: number
  expectedMeasurementPoints: number
  perRequestTimeoutMs: number
  overallTimeoutMs: number
  rawResults: CloudflareRawResults
}

export interface CloudflareQualityAssessment {
  completeness: 'complete' | 'partial' | 'none'
  stability: 'stable' | 'unstable' | 'unknown'
  trust: 'normal' | 'low' | 'insufficient'
  flags: string[]
  notes: string[]
}

export interface CloudflareTelemetrySessionLike {
  install(): void
  dispose(): void
  beginPhase(measurementId: number, type: string, at?: number): void
  endPhase(at?: number): void
  liveSummary(completedMeasurementPoints: number): CloudflareTelemetryLiveSummary
  snapshot(input: CloudflareTelemetrySnapshotInput): CloudflareDiagnostics
}

export interface CloudflareFetchTelemetryOptions {
  fetchImpl?: typeof fetch
  now?: () => number
  wallClock?: () => number
}

interface InFlightTelemetry {
  record: Omit<
    CloudflareRequestTelemetry,
    | 'endedAt'
    | 'endOffsetMs'
    | 'durationMs'
    | 'httpStatus'
    | 'retryAfter'
    | 'failed'
    | 'failureReason'
    | 'aborted'
    | 'is429'
    | 'is5xx'
    | 'responseContentLengthBytes'
  >
  signature: string
}

interface OpenPhaseTelemetry {
  measurementId: number
  type: string
  startOffsetMs: number
  startedAt: string
  idleBeforeMs: number
}

function parseContentLength(response: Response): number | null {
  const value = response.headers.get('content-length')
  if (!value) return null
  const parsed = Number(value)
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : null
}

function classifyRequest(input: RequestInfo | URL): {
  endpointClass: CloudflareEndpointClass
  hostClass: CloudflareHostClass
  queryKeys: string[]
  signature: string
} {
  const rawUrl = input instanceof Request ? input.url : input.toString()
  let url: URL
  try {
    url = new URL(rawUrl, 'http://local.invalid')
  } catch {
    return {
      endpointClass: 'other',
      hostClass: 'external',
      queryKeys: [],
      signature: 'other:invalid',
    }
  }

  const isLocalMock =
    url.hostname === '127.0.0.1' || url.hostname === 'localhost' || url.hostname === '::1'
  const hostClass: CloudflareHostClass =
    url.hostname === 'speed.cloudflare.com'
      ? 'cloudflare-speedtest'
      : isLocalMock
        ? 'local-mock'
        : 'external'
  const queryKeys: string[] = []
  url.searchParams.forEach((_value, key) => {
    if (['bytes', 'during', 'measId', 'scenario'].includes(key) && !queryKeys.includes(key)) {
      queryKeys.push(key)
    }
  })
  queryKeys.sort()

  let endpointClass: CloudflareEndpointClass = 'other'
  if (hostClass === 'cloudflare-speedtest' || hostClass === 'local-mock') {
    if (url.pathname === '/__down' && url.searchParams.get('bytes') === '0') {
      endpointClass = 'latency'
    } else if (url.pathname === '/__down') {
      endpointClass = 'download'
    } else if (url.pathname === '/__up') {
      endpointClass = 'upload'
    } else if (url.pathname === '/__results') {
      endpointClass = 'results'
    } else if (url.pathname.includes('turn')) {
      endpointClass = 'turn'
    }
  }

  const bytes = url.searchParams.get('bytes') ?? ''
  const during = url.searchParams.get('during') ?? ''
  return {
    endpointClass,
    hostClass,
    queryKeys,
    signature: `${endpointClass}:${url.pathname}:${bytes}:${during}`,
  }
}

function bodyTypeOf(init: RequestInit | undefined, input: RequestInfo | URL): string {
  const body = init?.body ?? (input instanceof Request ? input.body : null)
  if (body === null || body === undefined) return 'none'
  if (typeof body === 'string') return 'string'
  if (body instanceof ArrayBuffer) return 'arraybuffer'
  if (ArrayBuffer.isView(body)) return 'typedarray'
  if (typeof Blob !== 'undefined' && body instanceof Blob) return 'blob'
  if (typeof FormData !== 'undefined' && body instanceof FormData) return 'formdata'
  if (typeof URLSearchParams !== 'undefined' && body instanceof URLSearchParams) {
    return 'urlsearchparams'
  }
  if (typeof ReadableStream !== 'undefined' && body instanceof ReadableStream) return 'stream'
  return 'other'
}

function failureName(error: unknown): string {
  if (error instanceof Error && error.name) return error.name
  if (
    typeof error === 'object' &&
    error !== null &&
    'name' in error &&
    typeof error.name === 'string' &&
    error.name
  ) {
    return error.name
  }
  return 'request_failed'
}

export class CloudflareFetchTelemetry implements CloudflareTelemetrySessionLike {
  readonly #fetchImpl: typeof fetch | undefined
  readonly #now: () => number
  readonly #wallClock: () => number

  #installed = false
  #originalFetch: typeof fetch | undefined
  #wrappedFetch: typeof fetch | undefined
  #runStartOffsetMs = 0
  #runStartWallMs = 0
  #requestCounter = 0
  #requests: CloudflareRequestTelemetry[] = []
  #inFlight = new Map<number, InFlightTelemetry>()
  #lastCompleted: { signature: string; status: number | null } | null = null
  #lastRequestEndOffsetMs: number | null = null
  #openPhase: OpenPhaseTelemetry | null = null
  #phases: CloudflarePhaseTelemetry[] = []

  constructor(options: CloudflareFetchTelemetryOptions = {}) {
    this.#fetchImpl = options.fetchImpl
    this.#now = options.now ?? (() => performance.now())
    this.#wallClock = options.wallClock ?? (() => Date.now())
  }

  install(): void {
    if (this.#installed) return
    const target = globalThis as typeof globalThis & { fetch?: typeof fetch }
    const originalFetch = target.fetch
    const delegateFetch = this.#fetchImpl ?? originalFetch
    if (!originalFetch || !delegateFetch) throw new Error('fetch is unavailable')
    this.#originalFetch = originalFetch
    this.#runStartOffsetMs = this.#now()
    this.#runStartWallMs = this.#wallClock()
    const wrappedFetch: typeof fetch = (input: RequestInfo | URL, init?: RequestInit) =>
      this.#observe(delegateFetch, input, init)
    this.#wrappedFetch = wrappedFetch
    target.fetch = wrappedFetch
    this.#installed = true
  }

  dispose(): void {
    const target = globalThis as typeof globalThis & { fetch?: typeof fetch }
    if (this.#installed && this.#originalFetch && target.fetch === this.#wrappedFetch) {
      target.fetch = this.#originalFetch
    }
    this.#installed = false
  }

  beginPhase(measurementId: number, type: string, at = this.#now()): void {
    this.endPhase(at)
    const startOffsetMs = at - this.#runStartOffsetMs
    const previousEnd = this.#phases.at(-1)?.endOffsetMs ?? null
    this.#openPhase = {
      measurementId,
      type,
      startOffsetMs,
      startedAt: this.#isoAt(startOffsetMs),
      idleBeforeMs: previousEnd === null ? 0 : Math.max(0, startOffsetMs - previousEnd),
    }
  }

  endPhase(at = this.#now()): void {
    const open = this.#openPhase
    if (!open) return
    const endOffsetMs = Math.max(open.startOffsetMs, at - this.#runStartOffsetMs)
    this.#phases.push({
      measurementId: open.measurementId,
      type: open.type,
      startedAt: open.startedAt,
      endedAt: this.#isoAt(endOffsetMs),
      startOffsetMs: open.startOffsetMs,
      endOffsetMs,
      durationMs: Math.max(0, endOffsetMs - open.startOffsetMs),
      idleBeforeMs: open.idleBeforeMs,
    })
    this.#openPhase = null
  }

  liveSummary(completedMeasurementPoints: number): CloudflareTelemetryLiveSummary {
    return {
      requestCount: this.#requests.length,
      failedRequestCount: this.#requests.filter((request) => request.failed).length,
      http429Count: this.#requests.filter((request) => request.is429).length,
      http5xxCount: this.#requests.filter((request) => request.is5xx).length,
      maxRequestDurationMs:
        this.#requests.length === 0
          ? null
          : Math.max(...this.#requests.map((request) => request.durationMs)),
      observableRetryCandidates: this.#requests.filter((request) => request.retryCandidate).length,
      completedMeasurementPoints,
    }
  }

  snapshot(input: CloudflareTelemetrySnapshotInput): CloudflareDiagnostics {
    this.endPhase()
    const startedOffsetMs = 0
    const endedOffsetMs = Math.max(
      this.#now() - this.#runStartOffsetMs,
      ...this.#requests.map((request) => request.endOffsetMs),
      ...this.#phases.map((phase) => phase.endOffsetMs),
    )
    const statusCounts: Record<string, number> = {}
    for (const request of this.#requests) {
      const key = request.httpStatus === null ? 'N/A' : String(request.httpStatus)
      statusCounts[key] = (statusCounts[key] ?? 0) + 1
    }

    const completedMeasurementPoints =
      input.rawResults.unloadedLatencyPoints.length +
      input.rawResults.downloadBandwidthPoints.length +
      input.rawResults.uploadBandwidthPoints.length
    const slowPointDurations = [
      ...input.rawResults.downloadBandwidthPoints,
      ...input.rawResults.uploadBandwidthPoints,
    ]
      .map((point) => point.durationMs)
      .filter((duration): duration is number => duration !== null)
    const maxMeasurementDurationMs =
      slowPointDurations.length > 0 ? Math.max(...slowPointDurations) : null
    const maxGapMs =
      this.#requests.length === 0
        ? null
        : Math.max(...this.#requests.map((request) => request.gapSincePreviousEndMs ?? 0))
    const quality = assessCloudflareQuality({
      status: input.status,
      completedMeasurementPoints,
      expectedMeasurementPoints: input.expectedMeasurementPoints,
      requests: this.#requests,
      maxMeasurementDurationMs,
      maxGapMs,
      perRequestTimeoutMs: input.perRequestTimeoutMs,
    })

    return {
      schema: CLOUDFLARE_DIAGNOSTICS_SCHEMA,
      provider: 'cloudflare-speedtest',
      providerVersion: '1.14.1',
      status: input.status,
      error: input.error,
      startedAt: this.#isoAt(startedOffsetMs),
      completedAt: this.#isoAt(endedOffsetMs),
      durationMs: endedOffsetMs,
      configuredPayloadBytes: input.configuredPayloadBytes,
      requests: this.#requests,
      phases: this.#phases,
      rawResults: input.rawResults,
      summary: {
        ...this.liveSummary(completedMeasurementPoints),
        abortedRequestCount: this.#requests.filter((request) => request.aborted).length,
        statusCounts,
        observableResponseContentLengthBytes: this.#requests.reduce(
          (total, request) => total + (request.responseContentLengthBytes ?? 0),
          0,
        ),
        latencyRequestCount: this.#requests.filter(
          (request) => request.endpointClass === 'latency',
        ).length,
        downloadRequestCount: this.#requests.filter(
          (request) => request.endpointClass === 'download',
        ).length,
        uploadRequestCount: this.#requests.filter(
          (request) => request.endpointClass === 'upload',
        ).length,
        otherRequestCount: this.#requests.filter(
          (request) => !['latency', 'download', 'upload'].includes(request.endpointClass),
        ).length,
      },
      quality,
      timeoutPolicy: {
        perRequestMs: input.perRequestTimeoutMs,
        overallMs: input.overallTimeoutMs,
      },
      measurementSemantics: {
        latency: 'provider_ttfb',
        jitter: 'mean_absolute_difference_consecutive_latency_samples',
        downloadBandwidth: 'sdk_configured_percentile_of_measured_requests',
        uploadBandwidth: 'sdk_configured_percentile_of_measured_requests',
        bandwidthPercentile: 0.9,
        bandwidthMinRequestDurationMs: 10,
        note:
          'SDK bandwidth is a configured percentile, not aggregate payload divided by a shared wall-clock window.',
      },
      privacy: {
        recordsFullUrl: false,
        recordsPublicIp: false,
        recordsHeaders: false,
        recordsAuthorization: false,
        recordsCookies: false,
        recordsRequestBody: false,
        responseBodyRead: false,
        note:
          'Only endpoint classes, whitelisted query keys, status and timing metadata are exported.',
      },
    }
  }

  async #observe(
    originalFetch: typeof fetch,
    input: RequestInfo | URL,
    init?: RequestInit,
  ): Promise<Response> {
    const requestNumber = ++this.#requestCounter
    const requestId = `cf-${String(requestNumber).padStart(4, '0')}`
    const startedOffsetMs = this.#now() - this.#runStartOffsetMs
    const classified = classifyRequest(input)
    const method =
      init?.method?.toUpperCase() ??
      (input instanceof Request ? input.method.toUpperCase() : 'GET')
    const signature = `${method}:${classified.signature}:${bodyTypeOf(init, input)}`
    const after429 =
      this.#lastCompleted?.status === 429 && this.#lastCompleted.signature === signature
    const inFlight: InFlightTelemetry = {
      signature,
      record: {
        requestId,
        endpointClass: classified.endpointClass,
        hostClass: classified.hostClass,
        method,
        queryKeys: classified.queryKeys,
        bodyType: bodyTypeOf(init, input),
        startedAt: this.#isoAt(startedOffsetMs),
        startOffsetMs: startedOffsetMs,
        gapSincePreviousEndMs:
          this.#lastRequestEndOffsetMs === null
            ? null
            : Math.max(0, startedOffsetMs - this.#lastRequestEndOffsetMs),
        after429,
        retryCandidate: after429,
      },
    }
    this.#inFlight.set(requestNumber, inFlight)

    try {
      const response = await originalFetch(input, init)
      const endedOffsetMs = this.#now() - this.#runStartOffsetMs
      const status = response.status
      const retryAfter = response.headers.get('retry-after')
      this.#complete(requestNumber, endedOffsetMs, {
        httpStatus: status,
        retryAfter,
        failed: false,
        failureReason: null,
        aborted: init?.signal?.aborted ?? false,
        is429: status === 429,
        is5xx: status >= 500 && status <= 599,
        responseContentLengthBytes: parseContentLength(response),
      })
      this.#lastCompleted = { signature, status }
      this.#lastRequestEndOffsetMs = endedOffsetMs
      return response
    } catch (error) {
      const endedOffsetMs = this.#now() - this.#runStartOffsetMs
      const aborted = (init?.signal?.aborted ?? false) || failureName(error) === 'AbortError'
      this.#complete(requestNumber, endedOffsetMs, {
        httpStatus: null,
        retryAfter: null,
        failed: true,
        failureReason: failureName(error),
        aborted,
        is429: false,
        is5xx: false,
        responseContentLengthBytes: null,
      })
      this.#lastCompleted = { signature, status: null }
      this.#lastRequestEndOffsetMs = endedOffsetMs
      throw error
    }
  }

  #complete(
    requestNumber: number,
    endOffsetMs: number,
    completion: Pick<
      CloudflareRequestTelemetry,
      | 'httpStatus'
      | 'retryAfter'
      | 'failed'
      | 'failureReason'
      | 'aborted'
      | 'is429'
      | 'is5xx'
      | 'responseContentLengthBytes'
    >,
  ): void {
    const inFlight = this.#inFlight.get(requestNumber)
    if (!inFlight) return
    this.#inFlight.delete(requestNumber)
    this.#requests.push({
      ...inFlight.record,
      endedAt: this.#isoAt(endOffsetMs),
      endOffsetMs,
      durationMs: Math.max(0, endOffsetMs - inFlight.record.startOffsetMs),
      ...completion,
    })
  }

  #isoAt(offsetMs: number): string {
    return new Date(this.#runStartWallMs + offsetMs).toISOString()
  }
}

export interface CloudflareQualityInput {
  status: CloudflareTelemetrySnapshotInput['status']
  completedMeasurementPoints: number
  expectedMeasurementPoints: number
  requests: CloudflareRequestTelemetry[]
  maxMeasurementDurationMs: number | null
  maxGapMs: number | null
  perRequestTimeoutMs: number
}

export function assessCloudflareQuality(input: CloudflareQualityInput): CloudflareQualityAssessment {
  const flags: string[] = []
  const notes: string[] = []
  if (input.status === 'timeout') flags.push('timeout')
  if (input.status === 'failed') flags.push('failed')
  if (input.status === 'cancelled') flags.push('cancelled')
  if (input.requests.some((request) => request.is429)) flags.push('http_429')
  if (input.requests.some((request) => request.is5xx)) flags.push('http_5xx')
  if (input.requests.some((request) => request.failed)) flags.push('request_failure')
  if (input.requests.some((request) => request.aborted)) flags.push('request_aborted')
  if (
    input.maxMeasurementDurationMs !== null &&
    input.maxMeasurementDurationMs > input.perRequestTimeoutMs
  ) {
    flags.push('slow_measurement')
  }
  if (input.maxGapMs !== null && input.maxGapMs > input.perRequestTimeoutMs) {
    flags.push('slow_request_gap')
  }
  if (input.completedMeasurementPoints < input.expectedMeasurementPoints) {
    flags.push('incomplete_measurements')
  }

  const completeness =
    input.status === 'completed' &&
    input.completedMeasurementPoints >= input.expectedMeasurementPoints
      ? 'complete'
      : input.completedMeasurementPoints > 0
        ? 'partial'
        : 'none'
  const stability =
    input.requests.length === 0
      ? 'unknown'
      : flags.some((flag) =>
            ['timeout', 'failed', 'http_429', 'http_5xx', 'request_failure', 'request_aborted', 'slow_measurement', 'slow_request_gap'].includes(
              flag,
            ),
          )
        ? 'unstable'
        : 'stable'
  const trust =
    completeness !== 'complete'
      ? 'insufficient'
      : stability === 'stable'
        ? 'normal'
        : 'low'

  if (completeness === 'complete') notes.push('All configured measurement points completed.')
  else notes.push('The run did not complete all configured measurement points.')
  if (stability === 'unstable') {
    notes.push('Slow requests, gaps, HTTP errors or aborts were observed; no samples were removed.')
  } else if (stability === 'unknown') {
    notes.push('No request-level telemetry was available for stability assessment.')
  }

  return { completeness, stability, trust, flags, notes }
}
