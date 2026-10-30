<template>
  <AppLayout>
    <TablePageLayout>
      <div class="space-y-6">
        <!-- Overview card -->
        <section
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
                  <span v-if="balances.accounts" class="ml-1 text-gray-400 dark:text-gray-500">
                    · {{ t('admin.accounts.upstreamBalances.accountsCount', { count: balances.accounts }) }}
                  </span>
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
                :key="item.base_url"
                class="inline-flex items-center gap-1"
                :class="item.has_balance ? 'text-gray-700 dark:text-gray-300' : 'text-gray-400 dark:text-gray-500'"
              >
                <span class="font-medium">{{ item.base_url }}:</span>
                <span class="tabular-nums" data-testid="upstream-balance-item">
                  {{ item.has_balance ? `$${(item.balance ?? 0).toFixed(2)}` : t('admin.accounts.upstreamBalances.unknown') }}
                </span>
              </span>
            </div>
            <p v-else-if="!loading" class="mt-4 text-sm text-gray-500 dark:text-gray-400">
              {{ t('admin.accounts.upstreamBalances.noData') }}
            </p>
          </div>
          <p class="mt-2 px-1 text-xs leading-5 text-gray-400 dark:text-gray-500">
            {{ t('admin.accounts.upstreamBalances.snapshotHint') }}
          </p>
        </section>

        <!-- Accounts table -->
        <section class="card">
          <div class="overflow-x-auto">
            <table class="w-full text-left text-sm">
              <thead>
                <tr class="border-b border-gray-200 text-xs uppercase tracking-wide text-gray-500 dark:border-dark-700 dark:text-gray-400">
                  <th class="px-5 py-4 font-medium">{{ t('admin.accounts.upstreamBalances.upstreamColumn') }}</th>
                  <th class="px-5 py-4 font-medium">{{ t('admin.accounts.upstreamBalances.accountsColumn') }}</th>
                  <th class="px-5 py-4 font-medium">{{ t('admin.accounts.table.status') }}</th>
                  <th class="px-5 py-4 text-right font-medium">{{ t('admin.accounts.upstreamBalances.balanceColumn') }}</th>
                  <th class="px-5 py-4 font-medium">{{ t('admin.accounts.upstreamBalances.receivedAtTitle') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr
                  v-for="item in balances.items"
                  :key="item.base_url"
                  class="border-b border-gray-100 last:border-0 dark:border-dark-800"
                >
                  <td class="px-5 py-4 font-medium text-gray-900 dark:text-white">{{ item.base_url }}</td>
                  <td class="px-5 py-4 text-gray-700 dark:text-gray-300">
                    {{ item.ok_accounts }}/{{ item.accounts }}
                  </td>
                  <td class="px-5 py-4">
                    <span
                      class="inline-flex items-center gap-1 text-xs"
                      :class="item.probe_status === 'ok' ? 'text-green-600 dark:text-green-400' : (item.probe_status === 'partial' ? 'text-amber-600 dark:text-amber-400' : 'text-gray-500 dark:text-gray-400')"
                    >
                      {{ t(`admin.accounts.upstreamBalances.probe.${item.probe_status || 'unknown'}`) }}
                    </span>
                  </td>
                  <td class="px-5 py-4 text-right tabular-nums" :class="item.has_balance ? 'text-gray-900 dark:text-white' : 'text-gray-400 dark:text-gray-500'">
                    {{ item.has_balance ? `$${(item.balance ?? 0).toFixed(2)}` : t('admin.accounts.upstreamBalances.unknown') }}
                  </td>
                  <td class="px-5 py-4 text-gray-500 dark:text-gray-400">
                    {{ item.received_at ? formatReceivedAt(item.received_at) : '-' }}
                  </td>
                </tr>
              </tbody>
            </table>
            <div
              v-if="!balances.items.length && !loading"
              class="px-5 py-10 text-center text-sm text-gray-500 dark:text-gray-400"
            >
              {{ t('admin.accounts.upstreamBalances.noData') }}
            </div>
          </div>
        </section>
      </div>
    </TablePageLayout>
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Icon from '@/components/icons/Icon.vue'
import type { UpstreamBalancesResponse } from '@/types'

const { t } = useI18n()
const appStore = useAppStore()

const balances = ref<UpstreamBalancesResponse>({ items: [], total: 0, has_total: false, probe_failed: 0, accounts: 0 })
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

const formatReceivedAt = (value: string) => {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

onMounted(() => {
  void loadBalances()
})
</script>
