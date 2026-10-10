import { describe, expect, it, vi } from 'vitest'
import {
  assessCloudflareQuality,
  CloudflareFetchTelemetry,
  type CloudflareRequestTelemetry,
  type CloudflareRawResults,
} from './cloudflareTelemetry'

const emptyRawResults: CloudflareRawResults = {
  unloadedLatencyPoints: [],
  downloadBandwidthPoints: [],
  uploadBandwidthPoints: [],
  totalDurationMs: null,
}

function snapshot(session: CloudflareFetchTelemetry) {
  return session.snapshot({
    status: 'completed',
    error: null,
    configuredPayloadBytes: 40 * 1024 * 1024,
    expectedMeasurementPoints: 24,
    perRequestTimeoutMs: 20_000,
    overallTimeoutMs: 90_000,
    rawResults: emptyRawResults,
  })
}

describe('CloudflareFetchTelemetry', () => {
  it('records a normal request without reading or copying the response body', async () => {
    const fakeFetch = vi.fn(async () => new Response('hello', { status: 200 }))
    const originalFetch = globalThis.fetch
    const session = new CloudflareFetchTelemetry({
      fetchImpl: fakeFetch as unknown as typeof fetch,
    })
    try {
      session.install()
      const response = await fetch(
        'https://speed.cloudflare.com/__down?bytes=5&token=secret-value',
        {
          method: 'GET',
          headers: {
            Authorization: 'Bearer secret-value',
            Cookie: 'secret-cookie',
          },
        },
      )
      expect(await response.text()).toBe('hello')
      const diagnostics = snapshot(session)

      expect(diagnostics.requests).toHaveLength(1)
      expect(diagnostics.requests[0]).toMatchObject({
        endpointClass: 'download',
        hostClass: 'cloudflare-speedtest',
        method: 'GET',
        queryKeys: ['bytes'],
        bodyType: 'none',
        httpStatus: 200,
        failed: false,
        aborted: false,
        is429: false,
        is5xx: false,
      })
      expect(diagnostics.privacy).toMatchObject({
        recordsFullUrl: false,
        recordsAuthorization: false,
        recordsCookies: false,
        recordsRequestBody: false,
        responseBodyRead: false,
      })
      expect(JSON.stringify(diagnostics)).not.toContain('secret-value')
      expect(JSON.stringify(diagnostics)).not.toContain('secret-cookie')
      expect(fakeFetch).toHaveBeenCalledTimes(1)
    } finally {
      session.dispose()
      expect(globalThis.fetch).toBe(originalFetch)
    }
  })

  it('classifies localhost mock endpoints without exposing the URL', async () => {
    const fakeFetch = vi.fn(async () => new Response('hello', { status: 200 }))
    const session = new CloudflareFetchTelemetry({
      fetchImpl: fakeFetch as unknown as typeof fetch,
    })
    try {
      session.install()
      await fetch('http://127.0.0.1:18080/__down?bytes=5&scenario=normal&token=secret')
      const diagnostics = snapshot(session)

      expect(diagnostics.requests[0]).toMatchObject({
        endpointClass: 'download',
        hostClass: 'local-mock',
        queryKeys: ['bytes', 'scenario'],
      })
      expect(JSON.stringify(diagnostics)).not.toContain('secret')
      expect(JSON.stringify(diagnostics)).not.toContain('127.0.0.1')
    } finally {
      session.dispose()
    }
  })

  it('records 429, Retry-After and an observable retry candidate', async () => {
    const fakeFetch = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(null, {
          status: 429,
          headers: { 'retry-after': '2' },
        }),
      )
      .mockResolvedValueOnce(new Response('ok', { status: 200 }))
    const session = new CloudflareFetchTelemetry({
      fetchImpl: fakeFetch as unknown as typeof fetch,
    })
    try {
      session.install()
      const url = 'https://speed.cloudflare.com/__down?bytes=1024'
      await fetch(url)
      await fetch(url)
      const diagnostics = snapshot(session)

      expect(diagnostics.summary.http429Count).toBe(1)
      expect(diagnostics.summary.observableRetryCandidates).toBe(1)
      expect(diagnostics.requests[0]).toMatchObject({
        httpStatus: 429,
        is429: true,
        retryAfter: '2',
        retryCandidate: false,
      })
      expect(diagnostics.requests[1]).toMatchObject({
        httpStatus: 200,
        after429: true,
        retryCandidate: true,
      })
    } finally {
      session.dispose()
    }
  })

  it('does not mark repeated successful requests as retries', async () => {
    const fakeFetch = vi.fn(async () => new Response('ok', { status: 200 }))
    const session = new CloudflareFetchTelemetry({
      fetchImpl: fakeFetch as unknown as typeof fetch,
    })
    try {
      session.install()
      const url = 'https://speed.cloudflare.com/__down?bytes=1024'
      await fetch(url)
      await fetch(url)
      const diagnostics = snapshot(session)

      expect(diagnostics.summary.http429Count).toBe(0)
      expect(diagnostics.summary.observableRetryCandidates).toBe(0)
      expect(diagnostics.requests.every((request) => !request.retryCandidate)).toBe(true)
    } finally {
      session.dispose()
    }
  })

  it('records HTTP 5xx and network failures without exporting the error URL', async () => {
    const fakeFetch = vi
      .fn()
      .mockResolvedValueOnce(new Response(null, { status: 503 }))
      .mockRejectedValueOnce(new TypeError('failed to fetch https://speed.cloudflare.com/secret'))
    const session = new CloudflareFetchTelemetry({
      fetchImpl: fakeFetch as unknown as typeof fetch,
    })
    try {
      session.install()
      await fetch('https://speed.cloudflare.com/__down?bytes=1')
      await expect(fetch('https://speed.cloudflare.com/__up')).rejects.toThrow('failed to fetch')
      const diagnostics = snapshot(session)

      expect(diagnostics.summary.http5xxCount).toBe(1)
      expect(diagnostics.summary.failedRequestCount).toBe(1)
      expect(diagnostics.requests[1]).toMatchObject({
        httpStatus: null,
        failed: true,
        failureReason: 'TypeError',
      })
      expect(JSON.stringify(diagnostics)).not.toContain('/secret')
    } finally {
      session.dispose()
    }
  })

  it('records an aborted request', async () => {
    const fakeFetch = vi.fn((_input: RequestInfo | URL, init?: RequestInit) =>
      Promise.reject(
        init?.signal?.aborted
          ? new DOMException('The operation was aborted.', 'AbortError')
          : new Error('not aborted'),
      ),
    )
    const session = new CloudflareFetchTelemetry({
      fetchImpl: fakeFetch as unknown as typeof fetch,
    })
    try {
      session.install()
      const controller = new AbortController()
      controller.abort()
      await expect(
        fetch('https://speed.cloudflare.com/__down?bytes=1', {
          signal: controller.signal,
        }),
      ).rejects.toMatchObject({ name: 'AbortError' })
      const diagnostics = snapshot(session)

      expect(diagnostics.summary.abortedRequestCount).toBe(1)
      expect(diagnostics.requests[0]).toMatchObject({
        failed: true,
        aborted: true,
        failureReason: 'AbortError',
      })
    } finally {
      session.dispose()
    }
  })

  it('records a timeout failure separately from an abort', async () => {
    const fakeFetch = vi.fn(() =>
      Promise.reject(new DOMException('The operation timed out.', 'TimeoutError')),
    )
    const session = new CloudflareFetchTelemetry({
      fetchImpl: fakeFetch as unknown as typeof fetch,
    })
    try {
      session.install()
      await expect(fetch('https://speed.cloudflare.com/__down?bytes=1')).rejects.toMatchObject({
        name: 'TimeoutError',
      })
      const diagnostics = snapshot(session)

      expect(diagnostics.summary.failedRequestCount).toBe(1)
      expect(diagnostics.summary.abortedRequestCount).toBe(0)
      expect(diagnostics.requests[0]).toMatchObject({
        failed: true,
        aborted: false,
        failureReason: 'TimeoutError',
      })
    } finally {
      session.dispose()
    }
  })

  it('records phase duration and idle time', () => {
    let monotonic = 0
    let wall = 1_000_000
    const session = new CloudflareFetchTelemetry({
      now: () => monotonic,
      wallClock: () => wall,
    })

    monotonic = 10
    wall = 1_000_010
    session.beginPhase(0, 'latency')
    monotonic = 25
    wall = 1_000_025
    session.endPhase()
    monotonic = 40
    wall = 1_000_040
    session.beginPhase(1, 'download')
    monotonic = 65
    wall = 1_000_065
    session.endPhase()

    const diagnostics = snapshot(session)
    expect(diagnostics.phases).toHaveLength(2)
    expect(diagnostics.phases[0]).toMatchObject({
      type: 'latency',
      durationMs: 15,
      idleBeforeMs: 0,
    })
    expect(diagnostics.phases[1]).toMatchObject({
      type: 'download',
      durationMs: 25,
      idleBeforeMs: 15,
    })
  })
})

describe('assessCloudflareQuality', () => {
  const completedRequest = {
    is429: false,
    is5xx: false,
    failed: false,
    aborted: false,
  } as unknown as CloudflareRequestTelemetry

  it('marks a complete, stable run as normal', () => {
    expect(
      assessCloudflareQuality({
        status: 'completed',
        completedMeasurementPoints: 24,
        expectedMeasurementPoints: 24,
        requests: [completedRequest],
        maxMeasurementDurationMs: 2_000,
        maxGapMs: 500,
        perRequestTimeoutMs: 20_000,
      }),
    ).toMatchObject({
      completeness: 'complete',
      stability: 'stable',
      trust: 'normal',
      flags: [],
    })
  })

  it('retains slow samples and marks the run unstable', () => {
    const quality = assessCloudflareQuality({
      status: 'completed',
      completedMeasurementPoints: 24,
      expectedMeasurementPoints: 24,
      requests: [completedRequest],
      maxMeasurementDurationMs: 30_000,
      maxGapMs: 25_000,
      perRequestTimeoutMs: 20_000,
    })

    expect(quality).toMatchObject({
      completeness: 'complete',
      stability: 'unstable',
      trust: 'low',
    })
    expect(quality.flags).toContain('slow_measurement')
    expect(quality.flags).toContain('slow_request_gap')
  })

  it('marks timeout partial results as insufficient', () => {
    expect(
      assessCloudflareQuality({
        status: 'timeout',
        completedMeasurementPoints: 4,
        expectedMeasurementPoints: 24,
        requests: [completedRequest],
        maxMeasurementDurationMs: 20_001,
        maxGapMs: 0,
        perRequestTimeoutMs: 20_000,
      }),
    ).toMatchObject({
      completeness: 'partial',
      stability: 'unstable',
      trust: 'insufficient',
    })
  })
})
