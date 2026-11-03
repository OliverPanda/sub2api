<template>
  <AppLayout>
    <div class="space-y-4">
      <section class="grid gap-3 sm:grid-cols-2 xl:grid-cols-7">
        <div v-for="stat in summaryStats" :key="stat.key" class="rounded-md border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900">
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ stat.label }}</p>
          <p class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">{{ stat.value }}</p>
        </div>
      </section>

      <section class="rounded-md border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900">
        <div class="mb-3 flex items-center justify-between">
          <div><h2 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.affiliates.profit.overviewTitle') }}</h2><p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.affiliates.profit.overviewNote') }}</p></div>
          <span class="text-sm font-semibold text-emerald-600 dark:text-emerald-400">{{ formatMargin(overview?.totals.gross_profit, overview?.totals.revenue) }}</span>
        </div>
        <DataTable :columns="overviewColumns" :data="overview?.items || []" :loading="loading">
          <template #cell-revenue="{ row }"><Amount :value="row.revenue" /></template><template #cell-estimated_cost="{ row }"><Amount :value="row.estimated_cost" /></template><template #cell-gross_profit="{ row }"><Amount :value="row.gross_profit" strong /></template><template #cell-margin="{ row }"><span class="font-semibold text-emerald-600">{{ formatMargin(row.gross_profit, row.revenue) }}</span></template>
        </DataTable>
      </section>

      <TablePageLayout>
        <template #filters>
          <div class="flex flex-wrap items-center gap-3">
            <input v-model="filters.search" class="input w-full md:w-80" :placeholder="t('admin.affiliates.profit.search')" @input="debounceLoad" />
            <input v-model="filters.start_at" type="date" class="input w-full sm:w-44" :title="t('admin.affiliates.records.startAt')" @change="reload" />
            <input v-model="filters.end_at" type="date" class="input w-full sm:w-44" :title="t('admin.affiliates.records.endAt')" @change="reload" />
            <label class="flex items-center gap-2 text-sm text-gray-600 dark:text-dark-300">
              <span>{{ t('admin.affiliates.profit.shareRate') }}</span>
              <input v-model.number="filters.share_rate_percent" type="number" min="0" max="100" step="0.1" class="input w-24" @change="reload" />
              <span>%</span>
            </label>
            <button class="btn btn-secondary px-2 md:px-3" :disabled="loading" :title="t('common.refresh')" @click="loadReport">
              <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
            </button>
          </div>
          <p class="mt-2 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.affiliates.profit.note') }}</p>
        </template>

        <template #table>
          <DataTable :columns="columns" :data="report?.items || []" :loading="loading">
            <template #cell-inviter="{ row }">
              <div class="min-w-40">
                <div class="font-medium text-gray-900 dark:text-white">{{ row.inviter_email || row.inviter_username || `#${row.inviter_id}` }}</div>
                <div class="font-mono text-xs text-gray-500 dark:text-dark-400">{{ row.aff_code }}</div>
              </div>
            </template>
            <template #cell-upstream="{ row }">
              <div class="min-w-36">
                <div class="text-sm text-gray-900 dark:text-white">{{ row.account_name || `#${row.account_id}` }}</div>
                <div class="text-xs text-gray-500 dark:text-dark-400">#{{ row.account_id }}</div>
              </div>
            </template>
            <template #cell-channel="{ row }">
              <span v-if="row.channel_id" class="text-sm text-gray-700 dark:text-gray-300">{{ row.channel_name || `#${row.channel_id}` }}</span>
              <span v-else class="text-sm text-gray-400">-</span>
            </template>
            <template #cell-invitee_count="{ row }"><span class="text-sm">{{ row.invitee_count }}</span></template>
            <template #cell-request_count="{ row }"><span class="text-sm">{{ row.request_count }}</span></template>
            <template #cell-revenue="{ row }"><Amount :value="row.revenue" /></template>
            <template #cell-estimated_cost="{ row }"><Amount :value="row.estimated_cost" /></template>
            <template #cell-gross_profit="{ row }"><Amount :value="row.gross_profit" strong /></template>
            <template #cell-gross_profit_margin="{ row }"><span class="text-sm font-semibold text-emerald-600 dark:text-emerald-400">{{ formatMargin(row.gross_profit, row.revenue) }}</span></template>
            <template #cell-share_amount="{ row }"><Amount :value="row.share_amount" strong /></template>
            <template #empty>
              <div class="py-8 text-center text-sm text-gray-500 dark:text-dark-400">{{ t('admin.affiliates.profit.noData') }}</div>
            </template>
          </DataTable>
        </template>

        <template #pagination>
          <Pagination
            v-if="report && report.total > 0"
            :page="pagination.page"
            :total="report.total"
            :page-size="pagination.page_size"
            @update:page="changePage"
            @update:pageSize="changePageSize"
          />
        </template>
      </TablePageLayout>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Column } from '@/components/common/types'
import { affiliatesAPI, getProfitOverview, type AffiliateProfitOverview, type AffiliateProfitReport } from '@/api/admin/affiliates'
import { useAppStore } from '@/stores/app'
import { extractI18nErrorMessage } from '@/utils/apiError'

const { t } = useI18n()
const appStore = useAppStore()
const report = ref<AffiliateProfitReport | null>(null)
const overview = ref<AffiliateProfitOverview | null>(null)
const loading = ref(false)
const now = new Date()
function localDate(value: Date): string {
  const year = value.getFullYear()
  const month = String(value.getMonth() + 1).padStart(2, '0')
  const day = String(value.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}
const filters = reactive({
  search: '',
  start_at: localDate(new Date(now.getFullYear(), now.getMonth(), 1)),
  end_at: localDate(now),
  share_rate_percent: 30,
})
const pagination = reactive({ page: 1, page_size: 50 })
let debounceTimer: ReturnType<typeof setTimeout> | null = null

const columns = computed<Column[]>(() => [
  { key: 'inviter', label: t('admin.affiliates.records.inviter') },
  { key: 'upstream', label: t('admin.affiliates.profit.upstreamAccount') },
  { key: 'channel', label: t('admin.affiliates.profit.channel') },
  { key: 'invitee_count', label: t('admin.affiliates.profit.invitees') },
  { key: 'request_count', label: t('admin.affiliates.profit.requests') },
  { key: 'revenue', label: t('admin.affiliates.profit.revenue') },
  { key: 'estimated_cost', label: t('admin.affiliates.profit.estimatedCost') },
  { key: 'gross_profit', label: t('admin.affiliates.profit.grossProfit') },
  { key: 'gross_profit_margin', label: t('admin.affiliates.profit.grossProfitMargin') },
  { key: 'share_amount', label: t('admin.affiliates.profit.shareAmount') },
])
const overviewColumns = computed<Column[]>(() => [
  { key: 'group_name', label: t('admin.affiliates.profit.group') }, { key: 'model', label: t('admin.affiliates.profit.model') }, { key: 'account_name', label: t('admin.affiliates.profit.upstreamAccount') }, { key: 'channel_name', label: t('admin.affiliates.profit.channel') }, { key: 'request_count', label: t('admin.affiliates.profit.requests') }, { key: 'revenue', label: t('admin.affiliates.profit.revenue') }, { key: 'estimated_cost', label: t('admin.affiliates.profit.estimatedCost') }, { key: 'gross_profit', label: t('admin.affiliates.profit.grossProfit') }, { key: 'margin', label: t('admin.affiliates.profit.grossProfitMargin') },
])

const summaryStats = computed(() => {
  const totals = report.value?.totals
  return [
    { key: 'invitees', label: t('admin.affiliates.profit.invitees'), value: String(totals?.invitee_count ?? 0) },
    { key: 'requests', label: t('admin.affiliates.profit.requests'), value: String(totals?.request_count ?? 0) },
    { key: 'revenue', label: t('admin.affiliates.profit.revenue'), value: formatMoney(totals?.revenue) },
    { key: 'cost', label: t('admin.affiliates.profit.estimatedCost'), value: formatMoney(totals?.estimated_cost) },
    { key: 'profit', label: t('admin.affiliates.profit.grossProfit'), value: formatMoney(totals?.gross_profit) },
    { key: 'margin', label: t('admin.affiliates.profit.grossProfitMargin'), value: formatMargin(totals?.gross_profit, totals?.revenue) },
    { key: 'share', label: t('admin.affiliates.profit.shareAmount'), value: formatMoney(totals?.share_amount) },
  ]
})

function formatMoney(value: number | null | undefined): string {
  return `$${Number(value || 0).toFixed(4)}`
}

function formatMargin(profit: number | null | undefined, revenue: number | null | undefined): string {
  const total = Number(revenue || 0)
  return total > 0 ? `${((Number(profit || 0) / total) * 100).toFixed(2)}%` : '0.00%'
}

async function loadReport() {
  loading.value = true
  try {
    const params = {
      page: pagination.page,
      page_size: pagination.page_size,
      search: filters.search.trim() || undefined,
      start_at: filters.start_at || undefined,
      end_at: filters.end_at || undefined,
      share_rate_percent: Number(filters.share_rate_percent),
      timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    }
    const [nextReport, nextOverview] = await Promise.all([affiliatesAPI.getProfitReport(params), getProfitOverview(params)])
    report.value = nextReport
    overview.value = nextOverview
  } catch (error) {
    appStore.showError(extractI18nErrorMessage(error, t, 'admin.affiliates.errors', t('common.error')))
  } finally {
    loading.value = false
  }
}

function reload() {
  pagination.page = 1
  void loadReport()
}

function debounceLoad() {
  if (debounceTimer) clearTimeout(debounceTimer)
  debounceTimer = setTimeout(reload, 300)
}

function changePage(page: number) {
  pagination.page = page
  void loadReport()
}

function changePageSize(size: number) {
  pagination.page_size = size
  pagination.page = 1
  void loadReport()
}

const Amount = defineComponent({
  props: { value: { type: Number, default: 0 }, strong: { type: Boolean, default: false } },
  setup(props) {
    return () => h('span', {
      class: props.strong ? 'whitespace-nowrap text-sm font-semibold text-emerald-600 dark:text-emerald-400' : 'whitespace-nowrap text-sm text-gray-900 dark:text-white',
    }, formatMoney(props.value))
  },
})

onMounted(() => { void loadReport() })
</script>
