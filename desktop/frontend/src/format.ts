// Formatting helpers. Every helper renders "N/A" for unknown values; a missing
// measurement is never displayed as zero.

export function formatMbps(value: number | null | undefined, digits = 2): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return 'N/A'
  return value.toFixed(digits)
}

export function formatMs(value: number | null | undefined, digits = 2): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return 'N/A'
  return value.toFixed(digits)
}

export function formatFromNs(value: number | null | undefined, digits = 2): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return 'N/A'
  return formatMs(value / 1e6, digits)
}

export function formatPercent(value: number | null | undefined, digits = 2): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return 'N/A'
  return `${value.toFixed(digits)}%`
}

export function formatBytes(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value) || value < 0) return 'N/A'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let size = value
  let unit = 0
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024
    unit += 1
  }
  return unit === 0 ? `${value} B` : `${size.toFixed(2)} ${units[unit]}`
}

export function formatSecondsFromNs(value: number | null | undefined, digits = 2): string {
  if (value === null || value === undefined || !Number.isFinite(value) || value <= 0) return 'N/A'
  return `${(value / 1e9).toFixed(digits)} s`
}

export function formatSecondsFromMs(value: number | null | undefined, digits = 1): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return 'N/A'
  return `${(value / 1000).toFixed(digits)} s`
}

export function formatCount(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return 'N/A'
  return String(value)
}

export function formatTimestamp(value: string | null | undefined): string {
  if (!value) return 'N/A'
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return value
  return parsed.toLocaleString()
}

export const scopeLabels: Record<string, string> = {
  local: '本机回环',
  lan: '局域网',
  remote: '公网',
  unknown: '未知',
}

export const statusLabels: Record<string, string> = {
  healthy: '健康',
  degraded: '降级',
  unavailable: '不可用',
  unknown: '未探测',
}

export const runStateLabels: Record<string, string> = {
  idle: '空闲',
  checking: '检查节点',
  probing: '探测节点',
  cancelling: '正在取消',
  preparing: '准备中',
  latency_testing: '延迟测试',
  download_testing: '下载测试',
  upload_testing: '上传测试',
  completed: '已完成',
  failed: '失败',
  cancelled: '已取消',
}

export function runStateLabel(state: string | null | undefined): string {
  if (!state) return 'N/A'
  return runStateLabels[state] ?? state
}

export function statusLabel(status: string | null | undefined): string {
  if (!status) return 'N/A'
  return statusLabels[status] ?? status
}

export function scopeLabel(scope: string | null | undefined): string {
  if (!scope) return 'N/A'
  return scopeLabels[scope] ?? scope
}
