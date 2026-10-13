import { describe, expect, it } from 'vitest'
import { stopReasonLabel, stopReasonLabels } from './format'

describe('stopReasonLabel', () => {
  it('labels every stop reason the engine can emit', () => {
    expect(stopReasonLabel('requested_bytes_reached')).toBe('达到请求字节数')
    expect(stopReasonLabel('duration_elapsed')).toBe('测量窗口结束')
    expect(stopReasonLabel('server_eof')).toBe('服务端关闭连接')
    expect(stopReasonLabel('server_limit_reached')).toBe('达到节点声明的字节上限')
  })

  it('never invents a label for a value the panel does not know', () => {
    // An unknown reason must surface verbatim rather than silently becoming
    // N/A or an empty cell: hiding it would read as "nothing to report".
    expect(stopReasonLabel('some_future_reason')).toBe('some_future_reason')
    expect(stopReasonLabels['some_future_reason']).toBeUndefined()
  })

  it('renders N/A for a missing reason', () => {
    expect(stopReasonLabel(undefined)).toBe('N/A')
    expect(stopReasonLabel(null)).toBe('N/A')
  })
})