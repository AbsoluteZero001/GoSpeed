<script setup lang="ts">
import { CircleAlert, ShieldCheck, Waypoints } from '@lucide/vue'
import { computed, ref } from 'vue'
import type { NDT7ConsentState, ProviderNodeView } from '../types'
import { formatBytes } from '../format'

// NDT7Panel renders the M-Lab NDT7 path.
//
// The panel is a presentation layer and nothing more. The three gates - the
// durable consent, the single use run authorization and the daily limit -
// live in the backend, so a panel that lies cannot get a measurement
// through. What the panel does is make the decision visible and
// inconvenient to skip: the full disclosure has to be opened, the consent
// has to be given, and every single measurement needs its own confirmation.
//
// The nodes it renders carry no endpoint and no access token. The backend
// minted an opaque reference, the frontend names a node by that reference,
// and only the backend can turn a reference into an address.

const props = defineProps<{
  state: NDT7ConsentState | null
  nodes: ProviderNodeView[]
  busy: boolean
}>()

const emit = defineEmits<{
  (event: 'consent', agreed: boolean): void
  (event: 'discover'): void
  (event: 'authorize', payload: { ref: string; direction: string }): void
  (event: 'revoke'): void
}>()

/** noticeOpen controls whether the full disclosure is visible. */
const noticeOpen = ref(false)
/** selectedRef is the opaque reference of the node the user picked. */
const selectedRef = ref('')
/** direction is the direction the next measurement will run in. */
const direction = ref<'download' | 'upload'>('download')
/** confirming is true while the per-run confirmation is open. */
const confirming = ref(false)

const consented = computed(() => props.state?.agreed === true)
const limitReached = computed(
  () => props.state?.dailyAvailable === true && props.state.dailyRemaining <= 0,
)
const selected = computed(
  () => props.nodes.find((node) => node.ref === selectedRef.value) ?? null,
)
const canDiscover = computed(() => consented.value && !props.busy)
const canAuthorize = computed(
  () =>
    consented.value &&
    !props.busy &&
    !limitReached.value &&
    selected.value !== null &&
    selectedRef.value !== '',
)

/** budgetText describes the traffic one run may use. */
const budgetText = computed(() => {
  const budget = props.state?.budget
  if (!budget) return ''
  return `下载上限 ${formatBytes(budget.downloadBytes)} · 上传上限 ${formatBytes(
    budget.uploadBytes,
  )} · 合计 ${formatBytes(budget.totalBytes)}`
})

/** directionBudget is the cap that applies to the direction that will run. */
const directionBudget = computed(() => {
  const budget = props.state?.budget
  if (!budget) return 'N/A'
  return direction.value === 'download'
    ? formatBytes(budget.downloadBytes)
    : formatBytes(budget.uploadBytes)
})

function select(ref: string) {
  if (props.busy) return
  selectedRef.value = ref
  confirming.value = false
}

function openConfirmation() {
  if (!canAuthorize.value) return
  confirming.value = true
}

function cancelConfirmation() {
  confirming.value = false
  emit('revoke')
}

function confirmRun() {
  if (!selected.value || props.busy) return
  emit('authorize', { ref: selected.value.ref, direction: direction.value })
  confirming.value = false
}
</script>

<template>
  <section class="panel" data-testid="ndt7-panel">
    <h2 class="panel-title">
      M-Lab 公共测速（NDT7）
      <span class="hint">第三方公共节点 · 需逐次确认</span>
    </h2>

    <!-- The daily counter. It is shown for information only: the limit is
         enforced by the provider, and a display can never replace that. -->
    <div v-if="state" class="notice-line" data-testid="ndt7-daily">
      今日已用 <strong>{{ state.dailyUsed }}</strong> / {{ state.dailyLimit }} 次，
      剩余 <strong>{{ state.dailyRemaining }}</strong> 次
      <span class="notice-line dim">（按本机自然日计数，重启不会清零）</span>
    </div>
    <div v-if="state && !state.dailyAvailable" class="banner warn" data-testid="ndt7-daily-unavailable">
      <CircleAlert :size="14" />
      <span class="text">
        每日计数不可用，无法确认上限，因此拒绝测速：{{ state.dailyError ?? '未知原因' }}
      </span>
    </div>
    <div v-if="limitReached" class="banner warn" data-testid="ndt7-limit-reached">
      <CircleAlert :size="14" />
      <span class="text">
        今日的 NDT7 测速次数已达上限（M-Lab AUP 建议软件客户端每天不超过 4 次）。明天再测。
      </span>
    </div>

    <!-- The consent. The full wording is shown, never a summary of it. -->
    <div v-if="!consented" data-testid="ndt7-consent-block">
      <div class="banner warn">
        <CircleAlert :size="14" />
        <span class="text">
          M-Lab NDT7 测速会把你的公网 IP 与测量数据发布到 M-Lab 的公开数据集（无删除接口），
          并且每次测速都需要单独确认。请先阅读完整告知。
        </span>
      </div>
      <button type="button" class="btn small" data-testid="ndt7-notice-toggle" @click="noticeOpen = !noticeOpen">
        {{ noticeOpen ? '收起告知' : '查看完整告知' }}
      </button>
      <pre v-if="noticeOpen" class="ndt7-notice" data-testid="ndt7-notice">{{ state?.notice }}</pre>
      <div class="chip-row">
        <button
          type="button"
          class="btn small primary"
          data-testid="ndt7-consent-agree"
          :disabled="busy"
          @click="emit('consent', true)"
        >
          <ShieldCheck :size="13" />
          我已阅读并同意
        </button>
        <button
          type="button"
          class="btn small"
          data-testid="ndt7-consent-refuse"
          :disabled="busy"
          @click="emit('consent', false)"
        >
          不同意
        </button>
      </div>
      <div v-if="state" class="notice-line dim">告知版本：{{ state.policyVersion }}</div>
    </div>

    <template v-else>
      <div class="chip-row">
        <button
          type="button"
          class="btn small"
          data-testid="ndt7-discover"
          :disabled="!canDiscover"
          @click="emit('discover')"
        >
          <Waypoints :size="13" />
          发现附近的 M-Lab 节点
        </button>
      </div>

      <div v-if="nodes.length === 0" class="node-row disabled" data-testid="ndt7-empty">
        尚未发现节点。发现节点会向 M-Lab 发起一次 Locate 查询，该查询本身会暴露你的公网 IP。
      </div>
      <div v-else class="node-list" data-testid="ndt7-nodes">
        <article
          v-for="node in nodes"
          :key="node.ref"
          class="node-row"
          :class="{ selected: node.ref === selectedRef }"
          data-testid="ndt7-node"
          @click="select(node.ref)"
        >
          <div class="line1">
            <span class="name">{{ node.name }}</span>
            <span class="spacer" />
            <span class="pill local">第三方公共节点</span>
          </div>
          <div class="addr">{{ node.host }} · {{ node.servicePath }}</div>
          <div class="addr">M-Lab Locate 自动发现 · 与自建节点不同，数据进入公开数据集</div>
        </article>
      </div>

      <div class="field">
        <span class="field-label">本次测速方向（下载与上传分别确认）</span>
        <div class="chip-row" data-testid="ndt7-direction">
          <button
            type="button"
            class="chip"
            :class="{ active: direction === 'download' }"
            data-testid="ndt7-direction-download"
            :disabled="busy"
            @click="direction = 'download'"
          >
            下载
          </button>
          <button
            type="button"
            class="chip"
            :class="{ active: direction === 'upload' }"
            data-testid="ndt7-direction-upload"
            :disabled="busy"
            @click="direction = 'upload'"
          >
            上传
          </button>
        </div>
      </div>

      <div class="notice-line dim">单次流量预算：{{ budgetText }}</div>

      <button
        type="button"
        class="btn small primary block"
        data-testid="ndt7-start"
        :disabled="!canAuthorize"
        @click="openConfirmation"
      >
        确认并开始这一次测速
      </button>

      <div v-if="confirming && selected" class="banner info" data-testid="ndt7-confirm">
        <ShieldCheck :size="14" />
        <span class="text">
          <strong>本次测速确认</strong><br />
          节点：{{ selected.name }}（{{ selected.host }}）<br />
          方向：{{ direction === 'download' ? '下载' : '上传' }}，流量上限 {{ directionBudget }}<br />
          本次测速会向 M-Lab 公开你的公网 IP 与测量数据，且无法删除。<br />
          <strong>这次确认只授权这一次测速，用后即失效；每次测速都要重新确认。</strong>
        </span>
      </div>
      <div v-if="confirming" class="chip-row">
        <button type="button" class="btn small primary" data-testid="ndt7-confirm-run" @click="confirmRun">
          确认开始这一次测速
        </button>
        <button type="button" class="btn small" data-testid="ndt7-confirm-cancel" @click="cancelConfirmation">
          取消
        </button>
      </div>
    </template>
  </section>
</template>

<style scoped>
.ndt7-notice {
  max-height: 320px;
  overflow: auto;
  padding: 10px 12px;
  border: 1px solid var(--border-strong);
  border-radius: 8px;
  background: var(--panel-2);
  color: var(--text);
  font-size: 12px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
