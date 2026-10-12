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

export const measurementStatusLabels: Record<string, string> = {
  completed: '已完成',
  failed: '失败',
  cancelled: '已取消',
  timeout: '超时',
  budget_exceeded: '超出预算',
  consent_required: '需要授权',
}

export const completenessLabels: Record<string, string> = {
  complete: '完整',
  partial: '部分完成',
  incomplete: '不完整',
}

export const stabilityLabels: Record<string, string> = {
  normal: '正常',
  degraded: '降级',
  unknown: '未知',
}

export const trustLabels: Record<string, string> = {
  verified: '已核验',
  normal: '一般',
  unknown: '未知',
}

export const ownershipLabels: Record<string, string> = {
  self_hosted: '自托管',
  third_party: '第三方',
}

export const discoveryLabels: Record<string, string> = {
  configured: '用户配置',
  service: '服务发现',
}

export const metricTypeLabels: Record<string, string> = {
  http_rtt: 'HTTP 往返时延（非 ICMP Ping）',
  tcp_min_rtt: 'TCP 最小往返时延',
  summary_rtt: '提供方汇总往返时延',
  icmp_ping: 'ICMP Ping',
}

export const jitterTypeLabels: Record<string, string> = {
  consecutive_diff: '相邻样本平均绝对差',
  rtp_jitter: 'RTP 抖动',
}

export const stopReasonLabels: Record<string, string> = {
  requested_bytes_reached: '达到请求字节数',
  duration_elapsed: '测量窗口结束',
  server_eof: '服务端关闭连接',
}

export function measurementStatusLabel(status: string | null | undefined): string {
  if (!status) return 'N/A'
  return measurementStatusLabels[status] ?? status
}

export function completenessLabel(value: string | null | undefined): string {
  if (!value) return 'N/A'
  return completenessLabels[value] ?? value
}

export function stabilityLabel(value: string | null | undefined): string {
  if (!value) return 'N/A'
  return stabilityLabels[value] ?? value
}

export function trustLabel(value: string | null | undefined): string {
  if (!value) return 'N/A'
  return trustLabels[value] ?? value
}

export function ownershipLabel(value: string | null | undefined): string {
  if (!value) return 'N/A'
  return ownershipLabels[value] ?? value
}

export function discoveryLabel(value: string | null | undefined): string {
  if (!value) return 'N/A'
  return discoveryLabels[value] ?? value
}

export function metricTypeLabel(value: string | null | undefined): string {
  if (!value) return 'N/A'
  return metricTypeLabels[value] ?? value
}

export function jitterTypeLabel(value: string | null | undefined): string {
  if (!value) return 'N/A'
  return jitterTypeLabels[value] ?? value
}

export function stopReasonLabel(value: string | null | undefined): string {
  if (!value) return 'N/A'
  return stopReasonLabels[value] ?? value
}

/**
 * scopeWarning explains what a measured rate does and does not mean for a
 * given node scope. The wording is deliberately conservative: a loopback or
 * LAN run is never presented as an internet bandwidth measurement, and a self
 * hosted remote node is never presented as an official one.
 */
export function scopeWarning(scope: string | null | undefined, selfHosted: boolean): string | null {
  switch (scope) {
    case 'local':
      return '本机回环测速：速率代表本机吞吐量，不代表宽带速度。'
    case 'lan':
      return '局域网测速：速率不代表互联网宽带速度。'
    case 'remote':
      return selfHosted
        ? '公网目标测速：该节点由你自行配置，不是官方测速节点；结果受服务器带宽、路由与拥塞影响。'
        : '公网目标测速：结果受服务器带宽、路由与拥塞影响。'
    default:
      return '节点地址未归类（域名可能解析到任意地址），无法判断该结果是否经过公网。'
  }
}
