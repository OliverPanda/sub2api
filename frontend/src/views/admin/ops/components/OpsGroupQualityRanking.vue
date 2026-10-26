<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  opsAPI,
  type OpsGroupQualityItem,
  type OpsGroupQualityParams,
  type OpsGroupQualityResponse,
  type OpsQueryMode
} from '@/api/admin/ops'
import EmptyState from '@/components/common/EmptyState.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import { formatNumber } from '@/utils/format'

interface Props {
  platformFilter?: string
  groupIdFilter?: number | null
  timeRange?: string
  customStartTime?: string | null
  customEndTime?: string | null
  queryMode?: OpsQueryMode
  refreshToken: number
  minSample?: number
}

const props = withDefaults(defineProps<Props>(), {
  platformFilter: '',
  groupIdFilter: null,
  timeRange: '1h',
  customStartTime: null,
  customEndTime: null,
  queryMode: 'auto',
  minSample: 20
})

const emit = defineEmits<{
  (e: 'selectGroup', groupId: number): void
}>()

const { t } = useI18n()

const loading = ref(false)
const errorMessage = ref('')
const data = ref<OpsGroupQualityResponse | null>(null)
const showAll = ref(false)

let requestController: AbortController | null = null
let requestSeq = 0

const items = computed(() => data.value?.items ?? [])
const visibleItems = computed(() => (showAll.value ? items.value : items.value.slice(0, 10)))
const hiddenCount = computed(() => Math.max(0, items.value.length - visibleItems.value.length))

function buildParams(): OpsGroupQualityParams {
  const params: OpsGroupQualityParams = {
    platform: props.platformFilter || undefined,
    group_id: props.groupIdFilter ?? undefined,
    mode: props.queryMode,
    min_sample: props.minSample
  }

  if (props.timeRange === 'custom') {
    if (props.customStartTime && props.customEndTime) {
      params.start_time = props.customStartTime
      params.end_time = props.customEndTime
    } else {
      params.time_range = '1h'
    }
  } else if (['5m', '30m', '1h', '6h', '24h'].includes(props.timeRange)) {
    params.time_range = props.timeRange as OpsGroupQualityParams['time_range']
  } else {
    params.time_range = '1h'
  }

  return params
}

function cancellationCode(err: unknown): string {
  if (!err || typeof err !== 'object' || !('code' in err)) return ''
  return String((err as Record<string, unknown>).code || '')
}

async function fetchRanking() {
  requestController?.abort()
  requestController = new AbortController()
  requestSeq += 1
  const seq = requestSeq

  loading.value = true
  errorMessage.value = ''
  try {
    const response = await opsAPI.getGroupQualityRanking(buildParams(), {
      signal: requestController.signal
    })
    if (seq !== requestSeq) return
    data.value = response
  } catch (err) {
    if (seq !== requestSeq || cancellationCode(err) === 'ERR_CANCELED') return
    data.value = null
    errorMessage.value = t('admin.ops.groupQuality.failedToLoad')
  } finally {
    if (seq === requestSeq) loading.value = false
  }
}

function rateClass(rate: number, sufficient: boolean): string {
  if (!sufficient) return 'text-gray-400 dark:text-gray-500'
  if (rate >= 0.99) return 'text-emerald-600 dark:text-emerald-400'
  if (rate >= 0.95) return 'text-amber-600 dark:text-amber-400'
  return 'text-red-600 dark:text-red-400'
}

function rateBadgeClass(rate: number, sufficient: boolean): string {
  if (!sufficient) return 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-400'
  if (rate >= 0.99) return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-400'
  if (rate >= 0.95) return 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-400'
  return 'bg-red-50 text-red-700 dark:bg-red-900/20 dark:text-red-400'
}

function percent(rate: number): string {
  return `${(rate * 100).toFixed(2)}%`
}

function duration(value: number | null): string {
  if (value === null || !Number.isFinite(value)) return '—'
  if (value >= 1000) return `${(value / 1000).toFixed(2)}s`
  return `${Math.round(value)}ms`
}

function selectGroup(item: OpsGroupQualityItem) {
  emit('selectGroup', item.group_id)
}

watch(
  () => [
    props.platformFilter,
    props.groupIdFilter,
    props.timeRange,
    props.customStartTime,
    props.customEndTime,
    props.queryMode,
    props.refreshToken,
    props.minSample
  ],
  fetchRanking,
  { immediate: true }
)

onUnmounted(() => {
  requestController?.abort()
})
</script>

<template>
  <section class="overflow-hidden rounded-3xl bg-white shadow-sm ring-1 ring-gray-900/5 dark:bg-dark-800 dark:ring-dark-700">
    <div class="flex flex-col gap-3 border-b border-gray-100 px-6 py-5 dark:border-dark-700 sm:flex-row sm:items-center sm:justify-between">
      <div>
        <h3 class="flex items-center gap-2 text-sm font-bold text-gray-900 dark:text-white">
          <svg class="h-4 w-4 text-rose-500" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 4h18M6 8h12M9 12h6m-4 4h2" />
          </svg>
          {{ t('admin.ops.groupQuality.title') }}
          <HelpTooltip :content="t('admin.ops.tooltips.groupQuality')" />
        </h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.ops.groupQuality.description', { sample: data?.min_sample ?? props.minSample }) }}
        </p>
      </div>
      <div class="flex shrink-0 items-center gap-3 text-xs text-gray-500 dark:text-gray-400">
        <span v-if="data" class="rounded-full bg-amber-50 px-2.5 py-1 font-medium text-amber-700 dark:bg-amber-900/20 dark:text-amber-400">
          {{ t('admin.ops.groupQuality.insufficient', { count: data.insufficient_sample_count }) }}
        </span>
        <button
          type="button"
          class="rounded-lg border border-gray-200 bg-white px-2.5 py-1 font-semibold text-gray-600 hover:bg-gray-50 disabled:opacity-50 dark:border-dark-700 dark:bg-dark-900 dark:text-gray-300 dark:hover:bg-dark-700"
          :disabled="loading"
          @click="fetchRanking"
        >
          {{ t('admin.ops.groupQuality.refresh') }}
        </button>
      </div>
    </div>

    <div v-if="errorMessage" class="flex items-center justify-between gap-3 border-b border-red-100 bg-red-50 px-6 py-3 text-xs text-red-600 dark:border-red-900/30 dark:bg-red-900/20 dark:text-red-400">
      <span>{{ errorMessage }}</span>
      <button type="button" class="font-semibold underline" @click="fetchRanking">{{ t('admin.ops.groupQuality.retry') }}</button>
    </div>

    <div v-if="loading && !data" class="space-y-3 p-6">
      <div v-for="i in 5" :key="i" class="h-10 animate-pulse rounded-xl bg-gray-100 dark:bg-dark-700"></div>
    </div>

    <EmptyState v-else-if="!loading && items.length === 0" :title="t('admin.ops.groupQuality.empty')" />

    <div v-else class="overflow-x-auto">
      <table class="min-w-full divide-y divide-gray-100 text-left text-xs dark:divide-dark-700">
        <thead class="bg-gray-50/80 text-gray-500 dark:bg-dark-900/60 dark:text-gray-400">
          <tr>
            <th scope="col" class="px-5 py-3 font-semibold">{{ t('admin.ops.groupQuality.columns.group') }}</th>
            <th scope="col" class="px-4 py-3 text-right font-semibold">{{ t('admin.ops.groupQuality.columns.requests') }}</th>
            <th scope="col" class="px-4 py-3 text-right font-semibold">{{ t('admin.ops.groupQuality.columns.samples') }}</th>
            <th scope="col" class="px-4 py-3 text-right font-semibold">{{ t('admin.ops.groupQuality.columns.successRate') }}</th>
            <th scope="col" class="px-4 py-3 text-right font-semibold">{{ t('admin.ops.groupQuality.columns.upstreamErrorRate') }}</th>
            <th scope="col" class="px-4 py-3 text-right font-semibold">{{ t('admin.ops.groupQuality.columns.rateLimitRate') }}</th>
            <th scope="col" class="px-4 py-3 text-right font-semibold">{{ t('admin.ops.groupQuality.columns.p95Duration') }}</th>
            <th scope="col" class="px-4 py-3 text-right font-semibold">{{ t('admin.ops.groupQuality.columns.p95TTFT') }}</th>
            <th scope="col" class="px-5 py-3 text-right font-semibold">{{ t('admin.ops.groupQuality.columns.status') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
          <tr
            v-for="item in visibleItems"
            :key="item.group_id"
            class="transition-colors hover:bg-gray-50/80 dark:hover:bg-dark-700/40"
          >
            <td class="px-5 py-3">
              <button
                type="button"
                class="flex max-w-[280px] items-center gap-2 text-left"
                :title="t('admin.ops.groupQuality.selectHint')"
                @click="selectGroup(item)"
              >
                <span class="truncate font-semibold text-gray-900 hover:text-blue-600 dark:text-white dark:hover:text-blue-400">
                  {{ item.group_name || `#${item.group_id}` }}
                </span>
                <span class="shrink-0 rounded-md bg-gray-100 px-1.5 py-0.5 text-[10px] font-medium uppercase text-gray-500 dark:bg-dark-700 dark:text-gray-400">
                  {{ item.platform }}
                </span>
              </button>
            </td>
            <td class="whitespace-nowrap px-4 py-3 text-right tabular-nums text-gray-700 dark:text-gray-300">{{ formatNumber(item.request_count) }}</td>
            <td class="whitespace-nowrap px-4 py-3 text-right tabular-nums text-gray-700 dark:text-gray-300">{{ formatNumber(item.sample_count) }}</td>
            <td class="whitespace-nowrap px-4 py-3 text-right font-semibold tabular-nums" :class="rateClass(item.success_rate, item.sample_sufficient)">
              {{ percent(item.success_rate) }}
            </td>
            <td class="whitespace-nowrap px-4 py-3 text-right tabular-nums text-gray-700 dark:text-gray-300">{{ percent(item.upstream_error_rate) }}</td>
            <td class="whitespace-nowrap px-4 py-3 text-right tabular-nums text-gray-700 dark:text-gray-300">{{ percent(item.rate_limit_rate) }}</td>
            <td class="whitespace-nowrap px-4 py-3 text-right tabular-nums text-gray-700 dark:text-gray-300">{{ duration(item.p95_duration_ms) }}</td>
            <td class="whitespace-nowrap px-4 py-3 text-right tabular-nums text-gray-700 dark:text-gray-300">{{ duration(item.p95_ttft_ms) }}</td>
            <td class="whitespace-nowrap px-5 py-3 text-right">
              <span class="inline-flex rounded-full px-2 py-1 text-[10px] font-semibold" :class="rateBadgeClass(item.success_rate, item.sample_sufficient)">
                {{ item.sample_sufficient ? t('admin.ops.groupQuality.status.measured') : t('admin.ops.groupQuality.status.insufficient') }}
              </span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="hiddenCount > 0 || showAll" class="flex justify-center border-t border-gray-100 px-6 py-3 dark:border-dark-700">
      <button type="button" class="text-xs font-semibold text-blue-600 hover:text-blue-700 dark:text-blue-400" @click="showAll = !showAll">
        {{ showAll ? t('admin.ops.groupQuality.showTop') : t('admin.ops.groupQuality.showAll', { count: items.length }) }}
      </button>
    </div>
  </section>
</template>
