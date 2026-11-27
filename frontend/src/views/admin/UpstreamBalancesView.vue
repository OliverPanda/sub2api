<template>
  <AppLayout>
    <TablePageLayout>
      <section class="card">
        <div class="flex items-center justify-between border-b border-gray-200 px-5 py-4 dark:border-dark-700">
          <h2 class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t('admin.accounts.upstreamBalances.title') }}
          </h2>
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
        <div class="overflow-x-auto">
          <table class="w-full text-left text-sm">
            <thead>
              <tr class="border-b border-gray-200 text-xs uppercase tracking-wide text-gray-500 dark:border-dark-700 dark:text-gray-400">
                <th class="px-5 py-4 font-medium">{{ t('admin.accounts.upstreamBalances.upstreamColumn') }}</th>
                <th class="px-5 py-4 text-right font-medium">{{ t('admin.accounts.upstreamBalances.balanceColumn') }}</th>
                <th class="px-5 py-4 text-right font-medium">{{ t('admin.accounts.upstreamBalances.quotaColumn') }}</th>
                <th class="px-5 py-4 font-medium">{{ t('admin.accounts.upstreamBalances.groupsColumn') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="item in balances.items"
                :key="item.base_url"
                class="border-b border-gray-100 last:border-0 dark:border-dark-800"
              >
                <td class="px-5 py-4 font-medium text-gray-900 dark:text-white">{{ item.base_url }}</td>
                <td class="px-5 py-4 text-right tabular-nums" :class="item.has_balance ? 'text-gray-900 dark:text-white' : 'text-gray-400 dark:text-gray-500'">
                  {{ item.quota_unlimited ? t('admin.accounts.upstreamBalances.unlimited') : (item.has_balance ? `$${(item.balance ?? 0).toFixed(2)}` : t('admin.accounts.upstreamBalances.unknown')) }}
                </td>
                <td class="px-5 py-4 text-right tabular-nums text-gray-700 dark:text-gray-300">
                  {{ item.quota == null ? t('admin.accounts.upstreamBalances.unknown') : `$${item.quota_used?.toFixed(2) ?? '0.00'} / $${item.quota.toFixed(2)}` }}
                </td>
                <td class="px-5 py-4 text-gray-700 dark:text-gray-300">{{ item.groups.join(', ') || '-' }}</td>
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
import Icon from '@/components/icons/Icon.vue'
import type { UpstreamBalancesResponse } from '@/types'

const { t } = useI18n()
const appStore = useAppStore()
const balances = ref<UpstreamBalancesResponse>({ items: [] })
const loading = ref(false)

const loadBalances = async () => {
  if (loading.value) return
  loading.value = true
  try {
    balances.value = await adminAPI.accounts.getUpstreamBalances()
  } catch (error) {
    console.error('Failed to load upstream balances:', error)
    appStore.showError(t('admin.accounts.upstreamBalances.loadFailed'))
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void loadBalances()
})
</script>
