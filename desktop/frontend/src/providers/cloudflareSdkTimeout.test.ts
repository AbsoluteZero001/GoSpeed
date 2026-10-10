import { afterEach, describe, expect, it, vi } from 'vitest'
import SpeedTest from '@cloudflare/speedtest'

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function mockPerformanceTiming(): void {
  const timing = {
    transferSize: 4,
    requestStart: 0,
    responseStart: 10,
    responseEnd: 20,
    connectStart: 0,
    connectEnd: 1,
    secureConnectionStart: 0,
    nextHopProtocol: 'http/1.1',
  } as unknown as PerformanceResourceTiming
  vi.spyOn(performance, 'getEntriesByName').mockReturnValue([timing])
}

function delayedBodyFetch(delayMs: number) {
  return vi.fn((_input: RequestInfo | URL, init?: RequestInit) =>
    Promise.resolve(
      new Response(
        new ReadableStream<Uint8Array>({
          start(controller) {
            const timer = setTimeout(() => {
              controller.enqueue(new Uint8Array([1, 2, 3, 4]))
              controller.close()
            }, delayMs)
            init?.signal?.addEventListener(
              'abort',
              () => {
                clearTimeout(timer)
                controller.error(new DOMException('The operation was aborted.', 'AbortError'))
              },
              { once: true },
            )
          },
        }),
        {
          status: 200,
          headers: { 'content-length': '4' },
        },
      ),
    ),
  )
}

function createEngine(fetchImpl: ReturnType<typeof delayedBodyFetch>) {
  vi.stubGlobal('fetch', fetchImpl)
  return new SpeedTest({
    autoStart: false,
    logMeasurementApiUrl: null,
    logAimApiUrl: null,
    measureDownloadLoadedLatency: false,
    measureUploadLoadedLatency: false,
    bandwidthAbortRequestDuration: 20_000,
    measurements: [{ type: 'download', bytes: 1024, count: 1, bypassMinDuration: true }],
  })
}

describe('Cloudflare SDK bandwidthAbortRequestDuration', () => {
  it.each([2_000, 15_000])(
    'allows a slow response body that finishes in %i ms',
    async (delayMs) => {
      vi.useFakeTimers()
      mockPerformanceTiming()
      const fetchMock = delayedBodyFetch(delayMs)
      const engine = createEngine(fetchMock)
      const finished = new Promise<void>((resolve) => {
        engine.onFinish = () => resolve()
      })

      engine.play()
      await vi.runAllTimersAsync()
      await finished

      expect(fetchMock).toHaveBeenCalledTimes(1)
      expect(engine.isFinished).toBe(true)
      expect(engine.isRunning).toBe(false)
    },
  )

  it('aborts a response body that exceeds bandwidthAbortRequestDuration', async () => {
    vi.useFakeTimers()
    mockPerformanceTiming()
    const fetchMock = delayedBodyFetch(38_000)
    const engine = createEngine(fetchMock)
    const error = new Promise<string>((resolve) => {
      engine.onError = (message) => resolve(message)
    })
    const onFinish = vi.fn()
    engine.onFinish = onFinish

    engine.play()
    await vi.advanceTimersByTimeAsync(20_000)

    await expect(error).resolves.toContain('bandwidthAbortRequestDuration')
    await vi.advanceTimersByTimeAsync(20_000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(onFinish).not.toHaveBeenCalled()
    expect(engine.isRunning).toBe(false)
    expect(vi.getTimerCount()).toBe(0)

    engine.play()
    await vi.advanceTimersByTimeAsync(1_000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('reports an HTTP 200 response whose body read fails', async () => {
    vi.useFakeTimers()
    mockPerformanceTiming()
    const fetchMock = vi.fn(() =>
      Promise.resolve(
        new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              setTimeout(
                () => controller.error(new Error('mock body stream failure')),
                100,
              )
            },
          }),
          { status: 200 },
        ),
      ),
    )
    vi.stubGlobal('fetch', fetchMock)
    const engine = new SpeedTest({
      autoStart: false,
      logMeasurementApiUrl: null,
      logAimApiUrl: null,
      measureDownloadLoadedLatency: false,
      measureUploadLoadedLatency: false,
      bandwidthAbortRequestDuration: 20_000,
      measurements: [{ type: 'download', bytes: 1024, count: 1, bypassMinDuration: true }],
    })
    const error = new Promise<string>((resolve) => {
      engine.onError = (message) => resolve(message)
    })

    engine.play()
    await vi.advanceTimersByTimeAsync(100)

    await expect(error).resolves.toContain('Connection failed')
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(engine.isFinished).toBe(false)
  })
})
