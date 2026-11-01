// The only place that talks to the Wails runtime. The UI subscribes to events
// once and never polls the backend for measurement state.

import {
  AuthorizeNDT7Run,
  CancelNodeCheck,
  CancelTest,
  CheckNodes,
  DiscoverNDT7Nodes,
  GetAppInfo,
  GetNDT7State,
  GetStatus,
  ListNodes,
  RevokeNDT7Authorization,
  SetNDT7Consent,
  StartTest,
} from '../wailsjs/go/main/App'
import { EventsOff, EventsOn } from '../wailsjs/runtime/runtime'
import type {
  AppInfo,
  FinishedEvent,
  NDT7ConsentState,
  NDT7NodeListResult,
  NDT7RunAuthorization,
  NodeCheckResult,
  NodeListResult,
  ProgressEvent,
  StartOptions,
  StartTestResult,
  StateEvent,
  StatusResult,
  TargetEvent,
} from './types'

export const EventState = 'gospeed:state'
export const EventTarget = 'gospeed:target'
export const EventProgress = 'gospeed:progress'
export const EventFinished = 'gospeed:finished'
export const EventNodes = 'gospeed:nodes'

// hasRuntime reports whether the Go bindings are available. In a plain browser
// (npm run dev without Wails) they are not, and the UI says so instead of
// pretending to measure anything.
export function hasRuntime(): boolean {
  const candidate = window as unknown as { go?: { main?: { App?: unknown } } }
  return typeof candidate.go?.main?.App !== 'undefined'
}

export function getAppInfo(): Promise<AppInfo> {
  return GetAppInfo()
}

export function listNodes(): Promise<NodeListResult> {
  return ListNodes()
}

export function checkNodes(): Promise<NodeCheckResult> {
  return CheckNodes()
}

export function cancelNodeCheck(): Promise<void> {
  return CancelNodeCheck()
}

export function startTest(options: StartOptions): Promise<StartTestResult> {
  return StartTest(options)
}

export function cancelTest(): Promise<void> {
  return CancelTest()
}

export function getStatus(): Promise<StatusResult> {
  return GetStatus()
}

// The M-Lab NDT7 calls. The backend owns the three gates; the frontend only
// asks, shows and confirms.
//
// getNDT7State returns the consent decision, the full disclosure text and
// today's counter. It performs no network request.
export function getNDT7State(): Promise<NDT7ConsentState> {
  return GetNDT7State()
}

// setNDT7Consent records the user's decision about the M-Lab disclosure.
// Refusing withdraws the pending authorizations and the discovered nodes.
export function setNDT7Consent(agreed: boolean): Promise<NDT7ConsentState> {
  return SetNDT7Consent(agreed)
}

// discoverNDT7Nodes asks M-Lab which NDT7 servers are near the client. The
// query itself reveals the client public IP, so the backend refuses it
// without a current consent.
export function discoverNDT7Nodes(): Promise<NDT7NodeListResult> {
  return DiscoverNDT7Nodes()
}

// authorizeNDT7Run authorizes exactly one measurement against one node in
// one direction. The grant is consumed by the first matching run.
export function authorizeNDT7Run(ref: string, direction: string): Promise<NDT7RunAuthorization> {
  return AuthorizeNDT7Run(ref, direction)
}

// revokeNDT7Authorization drops every pending run authorization.
export function revokeNDT7Authorization(): Promise<void> {
  return RevokeNDT7Authorization()
}

export interface EventHandlers {
  onState(event: StateEvent): void
  onTarget(event: TargetEvent): void
  onProgress(event: ProgressEvent): void
  onFinished(event: FinishedEvent): void
  onNodes(event: NodeCheckResult): void
}

// subscribe registers every requested listener and returns the unsubscribe
// function.
export function subscribe(handlers: Partial<EventHandlers>): () => void {
  if (handlers.onState) EventsOn(EventState, handlers.onState)
  if (handlers.onTarget) EventsOn(EventTarget, handlers.onTarget)
  if (handlers.onProgress) EventsOn(EventProgress, handlers.onProgress)
  if (handlers.onFinished) EventsOn(EventFinished, handlers.onFinished)
  if (handlers.onNodes) EventsOn(EventNodes, handlers.onNodes)
  return () => {
    EventsOff(EventState)
    EventsOff(EventTarget)
    EventsOff(EventProgress)
    EventsOff(EventFinished)
    EventsOff(EventNodes)
  }
}

// errorMessage normalises a binding rejection into the exact backend message.
export function errorMessage(error: unknown): string {
  if (error instanceof Error) return error.message
  if (typeof error === 'string') return error
  return String(error)
}
