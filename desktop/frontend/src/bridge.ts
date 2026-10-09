// The only place that talks to the Wails runtime. The UI subscribes to events
// once and never polls the backend for measurement state.

import {
  CancelNodeCheck,
  CancelTest,
  CheckNodes,
  GetAppInfo,
  GetStatus,
  ListNodes,
  StartTest,
} from '../wailsjs/go/main/App'
import { EventsOff, EventsOn } from '../wailsjs/runtime/runtime'
import type {
  AppInfo,
  FinishedEvent,
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
