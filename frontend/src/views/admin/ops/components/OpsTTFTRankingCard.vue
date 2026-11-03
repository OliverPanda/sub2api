<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import {
  opsAPI,
  type OpsTTFTRankingDimension,
  type OpsTTFTRankingItem,
  type OpsTTFTRankingTimeRange
} from '@/api/admin/ops'
import { formatNumber } from '@/utils/format'

const props = defineProps<{ refreshToken: number }>()
const { t } = useI18n()

type SortKey = 'name' | 'platform' | 'request_count' | 'sample_count' | 'avg_ms' | 'p50_ms' | 'p95_ms' | 'p99_ms' | 'max_ms'

const dimension = ref<OpsTTFTRankingDimension>('group')
const timeRange = ref<OpsTTFTRankingTimeRange>('1h')
const sortKey = ref<SortKey>('p99_ms')
const sortDescending = ref(true)
const items = ref<OpsTTFTRankingItem[]>([])
const loading = ref(false)
const errorMessage = ref('')
let requestSequence = 0

const dimensionOptions = computed(() => [
  { value: 'group', label: t('admin.ops.ttftRanking.groups') },
  { value: 'account', label: t('admin.ops.ttftRanking.channels') }
])
const timeRangeOptions = computed(() => [
  { value: '5m', label: t('admin.ops.timeRange.5m') },
  { value: '30m', label: t('admin.ops.timeRange.30m') },
  { value: '1h', label: t('admin.ops.timeRange.1h') },
  { value: '6h', label: t('admin.ops.timeRange.6h') },
  { value: '24h', label: t('admin.ops.timeRange.24h') },
  { value: '7d', label: t('admin.ops.timeRange.7d') },
  { value: '30d', label: t('admin.ops.timeRange.30d') }
])

const sortedItems = computed(() => [...items.value].sort((a, b) => {
  const aValue = a[sortKey.value]
  const bValue = b[sortKey.value]
  if (aValue == null && bValue != null) return 1
  if (aValue != null && bValue == null) return -1
  if (aValue == null && bValue == null) return a.id - b.id
  const comparison = typeof aValue === 'string' && typeof bValue === 'string'
    ? aValue.localeCompare(bValue)
    : Number(aValue) - Number(bValue)
  return comparison === 0 ? a.id - b.id : comparison * (sortDescending.value ? -1 : 1)
}))

async function loadData() {
  const sequence = ++requestSequence
  loading.value = true
  errorMessage.value = ''
  try {
    const response = await opsAPI.getTTFTRanking({ time_range: timeRange.value, dimension: dimension.value })
    if (sequence === requestSequence) items.value = response.items ?? []
  } catch (error: any) {
    if (sequence === requestSequence) {
      items.value = []
      errorMessage.value = error?.message || t('admin.ops.ttftRanking.failedToLoad')
    }
  } finally {
    if (sequence === requestSequence) loading.value = false
  }
}

watch([dimension, timeRange, () => props.refreshToken], () => void loadData(), { immediate: true })

function setSort(key: SortKey) {
  if (sortKey.value === key) sortDescending.value = !sortDescending.value
  else {
    sortKey.value = key
    sortDescending.value = key !== 'name' && key !== 'platform'
  }
}

function sortArrow(key: SortKey): string {
  if (sortKey.value !== key) return '↕'
  return sortDescending.value ? '↓' : '↑'
}

function formatMs(value?: number | null): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return '-'
  return value >= 1000 ? `${(value / 1000).toFixed(2)} s` : `${formatNumber(Math.round(value))} ms`
}

function formatCount(value?: number | null): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return '-'
  return formatNumber(value)
}
</script>

<template>
  <section class="card p-4 md:p-5">
    <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
      <div>
        <h3 class="text-sm font-bold text-gray-900 dark:text-white">{{ t('admin.ops.ttftRanking.title') }}</h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.ttftRanking.hint') }}</p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <div class="w-36"><Select v-model="dimension" :options="dimensionOptions" /></div>
        <div class="w-36"><Select v-model="timeRange" :options="timeRangeOptions" /></div>
      </div>
    </div>

    <div v-if="errorMessage" class="mb-4 rounded-lg bg-red-50 px-3 py-2 text-xs text-red-600 dark:bg-red-900/20 dark:text-red-400">
      {{ errorMessage }}
    </div>
    <div v-if="loading && items.length === 0" class="py-8 text-center text-sm text-gray-500 dark:text-gray-400">
      {{ t('admin.ops.loadingText') }}
    </div>
    <EmptyState v-else-if="!loading && items.length === 0" :title="t('common.noData')" :description="t('admin.ops.ttftRanking.empty')" />
    <div v-else class="overflow-auto rounded-lg border border-gray-200 dark:border-dark-700" :class="loading ? 'opacity-60' : ''">
      <table class="min-w-[920px] w-full text-left text-xs">
        <thead class="sticky top-0 z-10 bg-white dark:bg-dark-800">
          <tr class="border-b border-gray-200 text-gray-500 dark:border-dark-700 dark:text-gray-400">
            <th v-for="key in (['name', 'platform', 'request_count', 'sample_count', 'avg_ms', 'p50_ms', 'p95_ms', 'p99_ms', 'max_ms'] as SortKey[])" :key="key" class="px-3 py-2 font-semibold">
              <button class="inline-flex items-center gap-1 whitespace-nowrap hover:text-gray-900 dark:hover:text-white" @click="setSort(key)">
                {{ t(`admin.ops.ttftRanking.columns.${key}`) }}
                <span aria-hidden="true" class="text-[10px]">{{ sortArrow(key) }}</span>
              </button>
            </th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in sortedItems" :key="`${dimension}-${row.id}`" class="border-b border-gray-100 text-gray-700 last:border-b-0 dark:border-dark-800 dark:text-gray-200">
            <td class="px-3 py-2 font-medium">{{ row.name }} <span class="text-gray-400">#{{ row.id }}</span></td>
            <td class="px-3 py-2">{{ row.platform || '-' }}</td>
            <td class="px-3 py-2 tabular-nums">{{ formatCount(row.request_count) }}</td>
            <td class="px-3 py-2 tabular-nums">
              <span>{{ formatCount(row.sample_count) }}</span>
              <span v-if="row.sample_count < 20" class="ml-1 rounded bg-amber-100 px-1.5 py-0.5 text-[10px] text-amber-800 dark:bg-amber-900/30 dark:text-amber-300">{{ t('admin.ops.ttftRanking.lowSample') }}</span>
            </td>
            <td class="px-3 py-2 tabular-nums">{{ formatMs(row.avg_ms) }}</td>
            <td class="px-3 py-2 tabular-nums">{{ formatMs(row.p50_ms) }}</td>
            <td class="px-3 py-2 tabular-nums">{{ formatMs(row.p95_ms) }}</td>
            <td class="px-3 py-2 font-semibold tabular-nums">{{ formatMs(row.p99_ms) }}</td>
            <td class="px-3 py-2 tabular-nums">{{ formatMs(row.max_ms) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>
