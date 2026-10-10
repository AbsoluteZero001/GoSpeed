import { afterEach, describe, expect, it, vi } from 'vitest'
import SpeedTest from '@cloudflare/speedtest'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('Cloudflare SDK optional result logging', () => {
  it('does not POST final results when both optional logging URLs are null', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    const engine = new SpeedTest({
      autoStart: false,
      logMeasurementApiUrl: null,
      logAimApiUrl: null,
      measurements: [],
    })
    const finished = new Promise<void>((resolve) => {
      engine.onFinish = () => resolve()
    })

    engine.play()
    await finished
    await Promise.resolve()

    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('does POST to the configured AIM endpoint when final logging is enabled', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response('{}', {
        status: 200,
        headers: { 'content-type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)
    const engine = new SpeedTest({
      autoStart: false,
      logMeasurementApiUrl: null,
      logAimApiUrl: 'https://example.test/__results',
      measurements: [],
    })
    const logged = new Promise<void>((resolve) => {
      engine.onResultsLogged = () => resolve()
    })

    engine.play()
    await logged

    expect(fetchMock).toHaveBeenCalledWith(
      'https://example.test/__results',
      expect.objectContaining({ method: 'POST' }),
    )
  })

  it('does not add measurement or final-result logging requests when both URLs are null', async () => {
    const urls: string[] = []
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = input.toString()
      urls.push(url)
      return Promise.resolve(
        new Response('x', {
          status: 200,
          headers: { 'server-timing': 'cfSpeedEdge;dur=1' },
        }),
      )
    })
    vi.stubGlobal('fetch', fetchMock)
    const timing = {
      transferSize: 1,
      requestStart: 0,
      responseStart: 10,
      responseEnd: 20,
      connectStart: 0,
      connectEnd: 1,
      secureConnectionStart: 0,
      nextHopProtocol: 'http/1.1',
    } as unknown as PerformanceResourceTiming
    vi.spyOn(performance, 'getEntriesByName').mockReturnValue([timing])
    const engine = new SpeedTest({
      autoStart: false,
      downloadApiUrl: 'https://example.test/__down',
      uploadApiUrl: 'https://example.test/__up',
      logMeasurementApiUrl: null,
      logAimApiUrl: null,
      measurements: [
        { type: 'download', bytes: 1, count: 1, bypassMinDuration: true },
        { type: 'upload', bytes: 1, count: 1, bypassMinDuration: true },
      ],
    })
    const finished = new Promise<void>((resolve) => {
      engine.onFinish = () => resolve()
    })

    engine.play()
    await finished

    expect(urls.some((url) => url.includes('/__results'))).toBe(false)
    expect(urls.some((url) => url.includes('measId='))).toBe(false)
    expect(urls.some((url) => url.includes('/__down'))).toBe(true)
    expect(urls.some((url) => url.includes('/__up'))).toBe(true)
  })
})
