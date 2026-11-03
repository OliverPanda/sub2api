<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { OpsCacheStats } from '@/api/admin/ops'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import { formatNumber } from '@/utils/format'

const props = defineProps<{
  cacheStats: OpsCacheStats | null
  loading: boolean
}>()

const { t } = useI18n()
const cached = computed(() => Math.max(0, props.cacheStats?.cache_read_tokens ?? 0))
const written = computed(() => Math.max(0, props.cacheStats?.cache_creation_tokens ?? 0))
const uncached = computed(() => Math.max(0, props.cacheStats?.input_tokens ?? 0))
const total = computed(() => cached.value + written.value + uncached.value)
const hasData = computed(() => total.value > 0 && props.cacheStats?.hit_rate != null)
const hitRatePercent = computed(() => Math.min(100, Math.max(0, (props.cacheStats?.hit_rate ?? 0) * 100)))
const hitRateLabel = computed(() => `${hitRatePercent.value.toFixed(1)}%`)
</script>

<template>
  <section class="rounded-3xl bg-white p-6 shadow-sm ring-1 ring-gray-900/5 dark:bg-dark-800 dark:ring-dark-700">
    <div class="mb-5 flex items-center gap-2">
      <h3 class="text-sm font-bold text-gray-900 dark:text-white">{{ t('admin.ops.cacheStats.title') }}</h3>
      <HelpTooltip :content="t('admin.ops.cacheStats.hint')" />
    </div>

    <div v-if="loading && !cacheStats" class="h-24 animate-pulse rounded-lg bg-gray-100 dark:bg-dark-700" />
    <div v-else-if="hasData" class="grid grid-cols-1 gap-6 md:grid-cols-[minmax(150px,0.7fr)_minmax(0,2fr)] md:items-center">
      <div>
        <div class="text-3xl font-semibold tabular-nums text-gray-900 dark:text-white">{{ hitRateLabel }}</div>
        <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          {{ formatNumber(cached) }} / {{ formatNumber(total) }} {{ t('admin.ops.cacheStats.inputTokens') }}
        </div>
      </div>

      <div class="min-w-0">
        <div class="mb-2 flex h-2 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700" :aria-label="t('admin.ops.cacheStats.distribution')">
          <div class="h-full bg-emerald-500" :style="{ width: `${(cached / total) * 100}%` }" />
          <div class="h-full bg-amber-400" :style="{ width: `${(written / total) * 100}%` }" />
          <div class="h-full bg-gray-400 dark:bg-gray-500" :style="{ width: `${(uncached / total) * 100}%` }" />
        </div>
        <div class="grid grid-cols-1 gap-x-5 gap-y-2 text-xs sm:grid-cols-3">
          <div class="min-w-0">
            <div class="flex items-center gap-1.5 text-gray-500 dark:text-gray-400"><span class="h-2 w-2 shrink-0 rounded-full bg-emerald-500" />{{ t('admin.ops.cacheStats.read') }}</div>
            <div class="mt-0.5 font-medium tabular-nums text-gray-800 dark:text-gray-200">{{ formatNumber(cached) }}</div>
          </div>
          <div class="min-w-0">
            <div class="flex items-center gap-1.5 text-gray-500 dark:text-gray-400"><span class="h-2 w-2 shrink-0 rounded-full bg-amber-400" />{{ t('admin.ops.cacheStats.write') }}</div>
            <div class="mt-0.5 font-medium tabular-nums text-gray-800 dark:text-gray-200">{{ formatNumber(written) }}</div>
          </div>
          <div class="min-w-0">
            <div class="flex items-center gap-1.5 text-gray-500 dark:text-gray-400"><span class="h-2 w-2 shrink-0 rounded-full bg-gray-400 dark:bg-gray-500" />{{ t('admin.ops.cacheStats.uncached') }}</div>
            <div class="mt-0.5 font-medium tabular-nums text-gray-800 dark:text-gray-200">{{ formatNumber(uncached) }}</div>
          </div>
        </div>
      </div>
    </div>
    <div v-else class="py-4 text-center text-sm text-gray-500 dark:text-gray-400">
      {{ t('admin.ops.cacheStats.empty') }}
    </div>
  </section>
</template>
