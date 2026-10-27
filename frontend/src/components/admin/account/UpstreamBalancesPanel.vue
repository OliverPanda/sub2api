<template>
  <section
    v-if="show"
    class="space-y-2"
    data-testid="upstream-balances-panel"
    :aria-label="t('admin.accounts.upstreamBalances.title')"
  >
    <div class="rounded-2xl border border-gray-200 bg-white p-5 shadow-sm dark:border-dark-700 dark:bg-dark-800 sm:p-6">
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div class="min-w-0">
          <h2 class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t('admin.accounts.upstreamBalances.title') }}
          </h2>
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.accounts.upstreamBalances.total') }}
          </p>
          <p
            class="mt-1 flex min-h-10 items-center text-3xl font-bold tracking-tight tabular-nums text-gray-900 dark:text-white"
            data-testid="upstream-balances-total"
          >
            <template v-if="balances.has_total">${{ balances.total.toFixed(2) }}</template>
            <template v-else-if="loading"><LoadingSpinner size="sm" /></template>
            <template v-else>-</template>
          </p>
        </div>
        <button
          class="btn btn-secondary px-3 py-2 text-sm"
          :disabled="loading"
          data-testid="upstream-balances-refresh"
          @click="loadBalances"
        >
          <Icon name="refresh" size="sm" :class="{ 'animate-spin': loading }" />
          <span class="ml-1">
            {{ loading ? t('admin.accounts.upstreamBalances.refreshing') : t('admin.accounts.upstreamBalances.refresh') }}
          </span>
        </button>
      </div>

      <div
        v-if="balances.probe_failed > 0"
        class="mt-4 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-700/40 dark:bg-amber-900/20 dark:text-amber-200"
      >
        {{ t('admin.accounts.upstreamBalances.probeFailedHint', { count: balances.probe_failed }) }}
      </div>
      <div
        v-else-if="balances.has_total && balances.items.some((item) => !item.has_balance)"
        class="mt-4 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-700/40 dark:bg-amber-900/20 dark:text-amber-200"
      >
        {{ t('admin.accounts.upstreamBalances.partialHint') }}
      </div>

      <div v-if="balances.items.length" class="mt-4 flex flex-wrap gap-x-6 gap-y-2 text-sm">
        <span
          v-for="item in balances.items"
          :key="item.account_id"
          class="inline-flex items-center gap-1"
          :class="item.has_balance ? 'text-gray-700 dark:text-gray-300' : 'text-gray-400 dark:text-gray-500'"
        >
          <span class="font-medium">{{ item.name }}:</span>
          <span class="tabular-nums" data-testid="upstream-balance-item">
            {{ item.has_balance ? `$${(item.balance ?? 0).toFixed(2)}` : t('admin.accounts.upstreamBalances.unknown') }}
          </span>
        </span>
      </div>
      <p v-else-if="!loading" class="mt-4 text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.upstreamBalances.noData') }}
      </p>
    </div>

    <p class="px-1 text-xs leading-5 text-gray-400 dark:text-gray-500">
      {{ t('admin.accounts.upstreamBalances.snapshotHint') }}
    </p>
  </section>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Icon from '@/components/icons/Icon.vue'
import type { UpstreamBalancesResponse } from '@/types'

const props = defineProps<{ show: boolean }>()
const { t } = useI18n()
const appStore = useAppStore()

const balances = ref<UpstreamBalancesResponse>({ items: [], total: 0, has_total: false, probe_failed: 0 })
const loading = ref(false)

const loadBalances = async () => {
  if (loading.value) return
  loading.value = true
  try {
    balances.value = await adminAPI.accounts.getUpstreamBalances(1, 100)
  } catch (error) {
    console.error('Failed to load upstream balances:', error)
    appStore.showError(t('admin.accounts.upstreamBalances.loadFailed'))
  } finally {
    loading.value = false
  }
}

watch(
  () => props.show,
  (isShown) => {
    if (isShown) void loadBalances()
  },
  { immediate: true }
)
</script>
