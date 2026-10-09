// Payload types pushed by the Go backend. They mirror desktop/types.go and the
// JSON shape of internal/speedtest.Result. Nothing here is invented by the
// frontend: every number comes from a backend event.

export interface AppInfo {
  version: string
  goVersion: string
  platform: string
  maxConnections: number
  defaultConnections: number
  defaultDurationMs: number
  defaultSampleIntervalMs: number
  minDurationMs: number
  maxDurationMs: number
  minSampleIntervalMs: number
  maxSampleIntervalMs: number
  latencySamples: number
  nodeConfigPath: string
  nodeConfigSource: string
  configSearchPaths: string[]
  nodeCount: number
}

export interface NodeView {
  id: string
  name: string
  baseUrl: string
  protocol: string
  enabled: boolean
  local: boolean
  provider?: string
  description?: string
  location: string
  networkScope: string
}

export interface NodeListResult {
  path: string
  source: string
  nodes: NodeView[]
}

export interface NodeStatusView {
  id: string
  name: string
  baseUrl: string
  protocol: string
  enabled: boolean
  local: boolean
  networkScope: string
  status: string
  httpRttMs?: number
  dnsMs?: number
  tcpMs?: number
  tlsMs?: number
  latencyMedianMs?: number
  latencySamples: number
  latencyBelowResolution: number
  latencyFailures: number
  attempts: number
  serverVersion?: string
  capabilities: boolean
  capabilitiesLegacy: boolean
  detail?: string
  checkedAt?: string
}

export interface NodeCheckResult {
  path: string
  checkedAt: string
  cancelled: boolean
  nodes: NodeStatusView[]
}

export interface StateEvent {
  state: string
  message?: string
}

export interface NodeTargetView {
  id: string
  name: string
  baseUrl: string
  protocol: string
  local: boolean
  networkScope: string
  healthStatus?: string
  healthLatencyMs?: number
}

export interface CandidateView {
  rank: number
  nodeId: string
  nodeName: string
  status: string
  latencyMs?: number
  reason: string
}

export interface TargetEvent {
  target: NodeTargetView
  selectionMethod: string
  selectionReason?: string
  selectionNote?: string
  candidates?: CandidateView[]
  configPath?: string
}

export interface ProgressEvent {
  state: string
  phase?: string
  stage: string
  elapsedMs: number
  bytes: number
  mbps: number
  instantMbps: number
  activeConnections: number
  samples: number
  budgetFraction?: number
  budgetRemainingMs?: number
  message?: string
}

export interface StartOptions {
  selectionMode: string
  nodeId: string
  connections: number
  durationMs: number
  sampleIntervalMs: number
}

export interface StartTestResult {
  started: boolean
  mode: string
  message?: string
}

export interface StatusResult {
  busy: boolean
  kind?: string
  state: string
  message?: string
}

// ---- measurement result (speedtest.Result) ----

export interface LatencyResult {
  type: string
  attempts: number
  successful_samples: number
  failed_samples: number
  min_ns: number
  average_ns: number
  max_ns: number
  jitter_ns: number | null
  samples_ns: number[] | null
  packet_loss_percent: number | null
  errors?: string[]
}

export interface Statistics {
  samples: number
  mean_mbps: number | null
  median_mbps: number | null
  min_mbps: number | null
  max_mbps: number | null
  stddev_mbps: number | null
  cv_percent: number | null
  stddev_kind?: string
}

export interface ConnectionReport {
  index: number
  state: string
  bytes: number
  server_confirmed_bytes?: number | null
  duration_ns: number
  server_duration_ns?: number
  error?: string
}

export interface TransferResult {
  bytes: number
  duration_ns: number
  mbps: number
  mb_per_second: number
  connections: number
  active_connections: number
  failed_connections: number
  measurement_window: string
  stop_reason: string
  statistics: Statistics
  connection_reports?: ConnectionReport[]
}

export interface UploadResult extends TransferResult {
  server_confirmed_bytes: number
  server_duration_ns: number
}

export interface ServerLimits {
  max_connections_per_test?: number
  max_concurrent_tests?: number
  max_duration_seconds?: number
  max_download_bytes?: number
  max_upload_bytes?: number
}

export interface CapabilityInfo {
  supported: boolean
  unsupported_endpoint?: boolean
  protocol_version?: number
  server_version?: string
  capabilities?: string[]
  limits?: ServerLimits
  error?: string
}

export interface TargetInfo {
  server_id: string
  server_name: string
  server_address: string
  protocol: string
  local: boolean
  network_scope: string
  selection_method?: string
  selection_reason?: string
  health_status?: string
  health_latency_ns?: number
  capabilities?: CapabilityInfo
}

export interface SettingsSnapshot {
  phases: string[]
  duration_ns: number
  timeout_ns: number
  max_bytes: number
  connections: number
  sample_interval_ns: number
  latency_samples: number
  latency_interval_ns: number
  warmup: boolean
}

export interface TestResult {
  test_id: string
  timestamp: string
  status: string
  error_message?: string
  warnings?: string[]
  target: TargetInfo
  settings: SettingsSnapshot
  latency?: LatencyResult | null
  download?: TransferResult | null
  upload?: UploadResult | null
}

export interface FinishedEvent {
  status: string
  testId?: string
  error?: string
  cancelled: boolean
  durationMs: number
  result?: TestResult | null
}
