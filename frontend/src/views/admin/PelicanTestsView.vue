<template>
  <AppLayout>
    <div class="w-full min-w-0 space-y-6 pb-8">
      <header class="page-header mb-0 rounded-3xl bg-white p-5 shadow-sm ring-1 ring-gray-900/5 dark:bg-dark-800 dark:ring-dark-700 sm:p-6">
        <div class="flex flex-wrap items-start justify-between gap-4">
          <div class="flex min-w-0 items-start gap-4">
            <div class="flex h-12 w-12 flex-shrink-0 items-center justify-center rounded-2xl bg-primary-100 text-primary-600 dark:bg-primary-900/30 dark:text-primary-400">
              <Icon name="beaker" size="lg" />
            </div>
            <div class="min-w-0">
              <h1 class="page-title text-xl font-black text-gray-900 dark:text-white">
                {{ t('admin.pelicanTests.title') }}
              </h1>
              <p class="mt-1 max-w-3xl text-sm leading-6 text-gray-500 dark:text-gray-400">
                {{ t('admin.pelicanTests.description') }}
              </p>
            </div>
          </div>
          <div class="flex items-center gap-2 rounded-full bg-gray-50 px-3 py-1.5 text-xs font-medium text-gray-500 dark:bg-dark-700 dark:text-gray-300">
            <span class="h-2 w-2 rounded-full bg-emerald-500"></span>
            {{ t('admin.pelicanTests.workerHint') }}
          </div>
        </div>
      </header>

      <div class="grid min-w-0 gap-6 xl:grid-cols-[380px_minmax(0,1fr)]">
        <div class="min-w-0 space-y-6">
          <section class="card p-5 sm:p-6">
            <div>
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">
                {{ t('admin.pelicanTests.launchTitle') }}
              </h2>
              <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">
                {{ t('admin.pelicanTests.launchDescription') }}
              </p>
            </div>

            <div class="mt-5 space-y-4">
              <div>
                <label class="input-label" for="pelican-group-select">
                  {{ t('admin.pelicanTests.groupLabel') }}
                </label>
                <Select
                  id="pelican-group-select"
                  v-model="selectedGroupID"
                  :options="groupOptions"
                  :placeholder="t('admin.pelicanTests.groupPlaceholder')"
                  :loading="groupsLoading"
                  :disabled="starting"
                  searchable
                />
                <p
                  v-if="selectedGroup"
                  class="mt-2 flex flex-wrap items-center gap-2 text-xs text-gray-500 dark:text-gray-400"
                >
                  <span class="badge badge-primary">
                    {{ selectedGroup.platform }}
                  </span>
                  <span>{{ t('admin.pelicanTests.activeAccounts', { count: selectedGroup.active_account_count ?? 0 }) }}</span>
                </p>
                <p
                  v-if="selectedGroup && (selectedGroup.active_account_count ?? 0) === 0"
                  class="mt-2 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs leading-5 text-amber-800 dark:border-amber-700/40 dark:bg-amber-900/20 dark:text-amber-200"
                >
                  {{ t('admin.pelicanTests.noActiveAccounts') }}
                </p>
              </div>

              <div>
                <label class="input-label" for="pelican-model-select">
                  {{ t('admin.pelicanTests.modelLabel') }}
                </label>
                <Select
                  id="pelican-model-select"
                  v-model="modelID"
                  :options="modelOptions"
                  :placeholder="t('admin.pelicanTests.modelPlaceholder')"
                  :search-placeholder="t('admin.pelicanTests.modelSearchPlaceholder')"
                  :loading="modelsLoading"
                  :disabled="!selectedGroup || starting"
                  searchable
                  creatable
                />
                <p class="input-hint">
                  {{ t('admin.pelicanTests.modelHint') }}
                </p>
              </div>

              <div>
                <label class="input-label" for="pelican-prompt">
                  {{ t('admin.pelicanTests.promptLabel') }}
                </label>
                <textarea
                  id="pelican-prompt"
                  v-model="prompt"
                  class="input min-h-32 resize-y leading-6"
                  :placeholder="t('admin.pelicanTests.defaultPrompt')"
                  :disabled="starting"
                  maxlength="12000"
                ></textarea>
                <p class="input-hint">
                  {{ t('admin.pelicanTests.promptHint') }}
                </p>
              </div>

              <button
                type="button"
                data-testid="pelican-start"
                class="btn btn-primary w-full"
                :disabled="!canStart || starting"
                @click="startTest"
              >
                <LoadingSpinner v-if="starting" size="sm" color="white" />
                <Icon v-else name="play" size="sm" />
                {{ starting ? t('admin.pelicanTests.starting') : t('admin.pelicanTests.start') }}
              </button>
            </div>
          </section>

          <section class="card overflow-hidden">
            <div class="card-header flex items-center justify-between gap-3">
              <div class="min-w-0">
                <h2 class="text-base font-semibold text-gray-900 dark:text-white">
                  {{ t('admin.pelicanTests.historyTitle') }}
                </h2>
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                  {{ t('admin.pelicanTests.historyHint') }}
                </p>
              </div>
              <button
                type="button"
                class="btn btn-secondary btn-sm flex-shrink-0"
                :disabled="runsLoading || detailLoading"
                :title="t('admin.pelicanTests.refresh')"
                @click="refreshAll"
              >
                <Icon name="refresh" size="sm" :class="{ 'animate-spin': runsLoading || detailLoading }" />
                <span class="hidden sm:inline">
                  {{ runsLoading || detailLoading ? t('admin.pelicanTests.refreshing') : t('admin.pelicanTests.refresh') }}
                </span>
              </button>
            </div>

            <div class="max-h-[720px] overflow-y-auto p-3">
              <div v-if="runsLoading && runs.length === 0" class="flex justify-center py-12">
                <LoadingSpinner />
              </div>
              <div
                v-else-if="runs.length === 0"
                class="rounded-xl border border-dashed border-gray-200 px-4 py-10 text-center dark:border-dark-600"
              >
                <Icon name="beaker" size="lg" class="mx-auto text-gray-300 dark:text-dark-500" />
                <p class="mt-3 text-sm font-medium text-gray-700 dark:text-gray-300">
                  {{ t('admin.pelicanTests.noHistory') }}
                </p>
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                  {{ t('admin.pelicanTests.emptyDescription') }}
                </p>
              </div>

              <div v-else class="space-y-2">
                <div
                  v-for="run in runs"
                  :key="run.id"
                  class="group flex items-stretch gap-1 rounded-xl border transition-colors"
                  :class="selectedRunID === run.id
                    ? 'border-primary-300 bg-primary-50/70 dark:border-primary-700 dark:bg-primary-900/20'
                    : 'border-transparent hover:border-gray-200 hover:bg-gray-50 dark:hover:border-dark-600 dark:hover:bg-dark-700/50'"
                >
                  <button
                    type="button"
                    class="min-w-0 flex-1 rounded-xl px-3 py-3 text-left focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/50"
                    @click="selectedRunID = run.id"
                  >
                    <div class="flex items-start justify-between gap-2">
                      <div class="min-w-0">
                        <p class="truncate text-sm font-semibold text-gray-900 dark:text-white">
                          {{ run.group_name || t('admin.pelicanTests.unknownGroup') }}
                        </p>
                        <p class="mt-0.5 truncate font-mono text-[11px] text-gray-500 dark:text-gray-400">
                          {{ run.model_id }}
                        </p>
                      </div>
                      <span :class="['badge flex-shrink-0', runStatusClass(run.status)]">
                        {{ t(`admin.pelicanTests.status.${run.status}`) }}
                      </span>
                    </div>
                    <div class="mt-2 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-gray-500 dark:text-gray-400">
                      <span>#{{ run.id }}</span>
                      <span class="text-gray-300 dark:text-dark-600">·</span>
                      <span>{{ formatDateTime(run.created_at) }}</span>
                    </div>
                    <p class="mt-1.5 text-xs text-gray-600 dark:text-gray-300">
                      {{ t('admin.pelicanTests.summary', {
                        success: run.success_count,
                        failed: run.failed_count,
                        skipped: run.skipped_count
                      }) }}
                    </p>
                  </button>
                  <button
                    v-if="run.status !== 'running'"
                    type="button"
                    class="my-2 mr-2 flex w-8 flex-shrink-0 items-center justify-center rounded-lg text-gray-400 transition-colors hover:bg-red-50 hover:text-red-600 focus:outline-none focus-visible:ring-2 focus-visible:ring-red-500/40 dark:hover:bg-red-900/20 dark:hover:text-red-400"
                    :title="t('admin.pelicanTests.deleteRun')"
                    :aria-label="t('admin.pelicanTests.deleteRun')"
                    @click="askDelete(run)"
                  >
                    <Icon name="trash" size="sm" />
                  </button>
                </div>
              </div>
            </div>
          </section>
        </div>

        <section class="card min-w-0 overflow-hidden">
          <div v-if="detailLoading && !selectedRun" class="flex min-h-[520px] items-center justify-center">
            <LoadingSpinner size="lg" />
          </div>

          <div
            v-else-if="!selectedRun"
            class="flex min-h-[520px] flex-col items-center justify-center px-6 py-16 text-center"
          >
            <div class="flex h-16 w-16 items-center justify-center rounded-2xl bg-gray-100 text-gray-400 dark:bg-dark-700 dark:text-dark-400">
              <Icon name="beaker" size="xl" />
            </div>
            <h2 class="mt-5 text-lg font-semibold text-gray-900 dark:text-white">
              {{ t('admin.pelicanTests.emptyTitle') }}
            </h2>
            <p class="mt-2 max-w-md text-sm leading-6 text-gray-500 dark:text-gray-400">
              {{ t('admin.pelicanTests.selectRunHint') }}
            </p>
          </div>

          <template v-else>
            <div class="border-b border-gray-100 p-5 dark:border-dark-700 sm:p-6">
              <div class="flex flex-wrap items-start justify-between gap-4">
                <div class="min-w-0">
                  <div class="flex flex-wrap items-center gap-2">
                    <h2 class="truncate text-lg font-semibold text-gray-900 dark:text-white">
                      {{ selectedRun.group_name || t('admin.pelicanTests.unknownGroup') }}
                    </h2>
                    <span :class="['badge', runStatusClass(selectedRun.status)]">
                      {{ t(`admin.pelicanTests.status.${selectedRun.status}`) }}
                    </span>
                    <span class="text-xs text-gray-400 dark:text-dark-500">#{{ selectedRun.id }}</span>
                  </div>
                  <p class="mt-1 break-all font-mono text-xs text-gray-500 dark:text-gray-400">
                    {{ selectedRun.model_id }}
                  </p>
                </div>
                <div class="flex items-center gap-2">
                  <button
                    type="button"
                    class="btn btn-secondary btn-sm"
                    :disabled="detailLoading"
                    :title="t('admin.pelicanTests.refresh')"
                    @click="loadRunDetail()"
                  >
                    <Icon name="refresh" size="sm" :class="{ 'animate-spin': detailLoading }" />
                    <span>{{ t('admin.pelicanTests.refresh') }}</span>
                  </button>
                  <button
                    v-if="selectedRun.status !== 'running'"
                    type="button"
                    class="btn btn-secondary btn-sm text-red-600 hover:text-red-700 dark:text-red-400 dark:hover:text-red-300"
                    @click="askDelete(selectedRun)"
                  >
                    <Icon name="trash" size="sm" />
                    {{ t('admin.pelicanTests.deleteRun') }}
                  </button>
                </div>
              </div>

              <div class="mt-5 grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
                <div class="rounded-xl bg-gray-50 p-3 dark:bg-dark-700/60">
                  <p class="text-[11px] font-medium uppercase tracking-wide text-gray-400 dark:text-dark-400">
                    {{ t('admin.pelicanTests.totalAccounts', { count: selectedRun.total_accounts }) }}
                  </p>
                  <p class="mt-1 font-semibold text-gray-900 dark:text-white">
                    {{ t('admin.pelicanTests.summary', {
                      success: selectedRun.success_count,
                      failed: selectedRun.failed_count,
                      skipped: selectedRun.skipped_count
                    }) }}
                  </p>
                </div>
                <div class="rounded-xl bg-gray-50 p-3 dark:bg-dark-700/60">
                  <p class="text-[11px] font-medium uppercase tracking-wide text-gray-400 dark:text-dark-400">
                    {{ t('admin.pelicanTests.startedAt') }}
                  </p>
                  <p class="mt-1 font-medium text-gray-900 dark:text-white">
                    {{ formatDateTime(selectedRun.started_at) }}
                  </p>
                </div>
                <div class="rounded-xl bg-gray-50 p-3 dark:bg-dark-700/60">
                  <p class="text-[11px] font-medium uppercase tracking-wide text-gray-400 dark:text-dark-400">
                    {{ t('admin.pelicanTests.finishedAt') }}
                  </p>
                  <p class="mt-1 font-medium text-gray-900 dark:text-white">
                    {{ selectedRun.finished_at ? formatDateTime(selectedRun.finished_at) : '-' }}
                  </p>
                </div>
                <div class="rounded-xl bg-gray-50 p-3 dark:bg-dark-700/60">
                  <p class="text-[11px] font-medium uppercase tracking-wide text-gray-400 dark:text-dark-400">
                    {{ t('admin.pelicanTests.progress', {
                      done: selectedRun.results.length,
                      total: resultTarget
                    }) }}
                  </p>
                  <div class="mt-2 h-1.5 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-600">
                    <div class="h-full rounded-full bg-primary-500 transition-all duration-500" :style="{ width: `${progressPercent}%` }"></div>
                  </div>
                </div>
              </div>

              <div
                v-if="selectedRun.status === 'running'"
                class="mt-4 flex items-center gap-2 rounded-xl border border-primary-200 bg-primary-50 px-3 py-2 text-xs text-primary-700 dark:border-primary-800/50 dark:bg-primary-900/20 dark:text-primary-300"
              >
                <LoadingSpinner size="sm" />
                {{ t('admin.pelicanTests.runningHint') }}
              </div>

              <div
                v-if="selectedRun.error_message"
                class="mt-4 rounded-xl border border-red-200 bg-red-50 px-3 py-2 text-xs leading-5 text-red-700 dark:border-red-800/50 dark:bg-red-900/20 dark:text-red-300"
              >
                <span class="font-semibold">{{ t('admin.pelicanTests.batchError') }}:</span>
                <span class="ml-1 break-words">{{ selectedRun.error_message }}</span>
              </div>

              <details class="mt-4 rounded-xl border border-gray-200 px-3 py-2 text-xs dark:border-dark-600">
                <summary class="cursor-pointer select-none font-medium text-gray-600 dark:text-gray-300">
                  {{ t('admin.pelicanTests.testPrompt') }}
                </summary>
                <pre class="mt-2 whitespace-pre-wrap break-words font-sans leading-5 text-gray-500 dark:text-gray-400">{{ selectedRun.prompt }}</pre>
              </details>
            </div>

            <div class="p-5 sm:p-6">
              <div class="mb-4 flex items-center justify-between gap-3">
                <h3 class="text-base font-semibold text-gray-900 dark:text-white">
                  {{ t('admin.pelicanTests.resultsTitle') }}
                </h3>
                <span class="badge badge-gray">
                  {{ selectedRun.results.length }} / {{ resultTarget }}
                </span>
              </div>

              <div v-if="selectedRun.results.length === 0" class="rounded-2xl border border-dashed border-gray-200 px-6 py-14 text-center dark:border-dark-600">
                <Icon name="chart" size="lg" class="mx-auto text-gray-300 dark:text-dark-500" />
                <p class="mt-3 text-sm font-medium text-gray-700 dark:text-gray-300">
                  {{ t('admin.pelicanTests.noResults') }}
                </p>
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                  {{ t('admin.pelicanTests.noResultsHint') }}
                </p>
              </div>

              <div v-else class="grid gap-4 2xl:grid-cols-2">
                <article
                  v-for="result in selectedRun.results"
                  :key="result.id"
                  class="min-w-0 overflow-hidden rounded-2xl border border-gray-200 bg-white dark:border-dark-600 dark:bg-dark-800"
                >
                  <div class="flex items-start justify-between gap-3 border-b border-gray-100 px-4 py-3 dark:border-dark-700">
                    <div class="min-w-0">
                      <p class="truncate text-sm font-semibold text-gray-900 dark:text-white" :title="result.account_name">
                        {{ result.account_name || t('admin.pelicanTests.unknownAccount') }}
                      </p>
                      <div class="mt-1 flex flex-wrap items-center gap-2 text-[11px] text-gray-500 dark:text-gray-400">
                        <span class="badge badge-gray">{{ result.platform || '-' }}</span>
                        <span>{{ t('admin.pelicanTests.latency') }}: {{ formatLatency(result.latency_ms) }}</span>
                      </div>
                    </div>
                    <span :class="['badge flex-shrink-0', resultStatusClass(result.status)]">
                      {{ t(`admin.pelicanTests.resultStatus.${result.status}`) }}
                    </span>
                  </div>

                  <div class="p-4">
                    <button
                      v-if="safeSvg(result)"
                      type="button"
                      class="group relative block w-full overflow-hidden rounded-xl border border-gray-200 bg-gray-50 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/50 dark:border-dark-600 dark:bg-dark-900"
                      :title="t('admin.pelicanTests.preview')"
                      :aria-label="t('admin.pelicanTests.previewTitle', { account: result.account_name })"
                      @click="openPreview(result)"
                    >
                      <iframe
                        :title="t('admin.pelicanTests.svgTitle', { account: result.account_name })"
                        :srcdoc="safeSvg(result)"
                        sandbox=""
                        referrerpolicy="no-referrer"
                        class="pointer-events-none h-56 w-full bg-white"
                      ></iframe>
                      <span class="absolute inset-0 flex items-end justify-end bg-gradient-to-t from-gray-950/50 via-transparent to-transparent p-3 opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100">
                        <span class="inline-flex items-center gap-1 rounded-lg bg-white/95 px-2.5 py-1 text-xs font-medium text-gray-700 shadow-sm dark:bg-dark-800/95 dark:text-gray-200">
                          <Icon name="eye" size="sm" />
                          {{ t('admin.pelicanTests.preview') }}
                        </span>
                      </span>
                    </button>

                    <a
                      v-else-if="result.image_url"
                      :href="result.image_url"
                      target="_blank"
                      rel="noopener noreferrer"
                      class="group relative block w-full overflow-hidden rounded-xl border border-gray-200 bg-gray-50 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/50 dark:border-dark-600 dark:bg-dark-900"
                      :title="t('admin.pelicanTests.preview')"
                      :aria-label="t('admin.pelicanTests.previewTitle', { account: result.account_name })"
                    >
                      <img
                        :src="result.image_url"
                        :alt="t('admin.pelicanTests.previewTitle', { account: result.account_name })"
                        class="h-56 w-full bg-white object-contain"
                        referrerpolicy="no-referrer"
                      >
                      <span class="absolute inset-0 flex items-end justify-end bg-gradient-to-t from-gray-950/50 via-transparent to-transparent p-3 opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100">
                        <span class="inline-flex items-center gap-1 rounded-lg bg-white/95 px-2.5 py-1 text-xs font-medium text-gray-700 shadow-sm dark:bg-dark-800/95 dark:text-gray-200">
                          <Icon name="eye" size="sm" />
                          {{ t('admin.pelicanTests.preview') }}
                        </span>
                      </span>
                    </a>

                    <div
                      v-else
                      class="flex h-40 flex-col items-center justify-center rounded-xl border border-dashed border-red-200 bg-red-50/60 px-4 text-center dark:border-red-800/50 dark:bg-red-900/10"
                    >
                      <Icon name="xCircle" size="lg" class="text-red-400" />
                      <p class="mt-2 text-xs leading-5 text-red-700 dark:text-red-300">
                        {{ result.error_message || t('admin.pelicanTests.resultFailedFallback') }}
                      </p>
                    </div>

                    <p
                      v-if="result.error_message && resultMedia(result)"
                      class="mt-3 break-words rounded-lg bg-red-50 px-3 py-2 text-xs leading-5 text-red-700 dark:bg-red-900/20 dark:text-red-300"
                    >
                      <span class="font-semibold">{{ t('admin.pelicanTests.resultError') }}:</span>
                      {{ result.error_message }}
                    </p>

                    <details v-if="result.raw_output" class="mt-3">
                      <summary class="cursor-pointer select-none text-xs font-medium text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200">
                        {{ t('admin.pelicanTests.rawOutput') }}
                      </summary>
                      <pre class="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-all rounded-lg bg-gray-950 p-3 font-mono text-[11px] leading-5 text-gray-200">{{ result.raw_output }}</pre>
                    </details>
                  </div>
                </article>
              </div>
            </div>
          </template>
        </section>
      </div>
    </div>

    <BaseDialog
      :show="previewResult !== null"
      :title="previewTitle"
      width="full"
      @close="previewResult = null"
    >
      <iframe
        v-if="previewSvg"
        :title="previewTitle"
        :srcdoc="previewSvg"
        sandbox=""
        referrerpolicy="no-referrer"
        class="h-[70vh] min-h-[420px] w-full rounded-xl bg-white"
      ></iframe>
      <img
        v-else-if="previewResult?.image_url"
        :src="previewResult.image_url"
        :alt="previewTitle"
        class="h-[70vh] min-h-[420px] w-full rounded-xl bg-white object-contain"
        referrerpolicy="no-referrer"
      >
      <div v-else class="py-16 text-center text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.pelicanTests.noPreview') }}
      </div>
    </BaseDialog>

    <ConfirmDialog
      :show="deleteDialog"
      :title="t('admin.pelicanTests.deleteTitle')"
      :message="deleteMessage"
      :confirm-text="t('common.delete')"
      :cancel-text="t('common.cancel')"
      :danger="true"
      @confirm="confirmDelete"
      @cancel="deleteDialog = false"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatDateTime } from '@/utils/format'
import { sanitizeSvg } from '@/utils/sanitize'
import type {
  AdminGroup,
  PelicanTestResult,
  PelicanTestResultStatus,
  PelicanTestRun,
  PelicanTestRunDetail,
  PelicanTestRunStatus
} from '@/types'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()
const appStore = useAppStore()

const groups = ref<AdminGroup[]>([])
const groupsLoading = ref(false)
const selectedGroupID = ref<number | null>(null)
const modelCandidates = ref<string[]>([])
const modelID = ref<string | null>(null)
const modelsLoading = ref(false)
const prompt = ref('')
const starting = ref(false)

const runs = ref<PelicanTestRun[]>([])
const runsLoading = ref(false)
const selectedRunID = ref<number | null>(null)
const selectedRun = ref<PelicanTestRunDetail | null>(null)
const detailLoading = ref(false)

const deleteDialog = ref(false)
const pendingDelete = ref<PelicanTestRun | null>(null)
const deleting = ref(false)

const previewResult = ref<PelicanTestResult | null>(null)

let detailRequestSeq = 0
let modelRequestSeq = 0
let pollingTimer: ReturnType<typeof setInterval> | null = null
const svgCache = new WeakMap<object, string>()

const selectedGroup = computed(() =>
  selectedGroupID.value === null
    ? null
    : groups.value.find((group) => group.id === selectedGroupID.value) ?? null
)

const groupOptions = computed(() =>
  groups.value.map((group) => ({
    value: group.id,
    label: `${group.name} · ${group.platform} · ${t('admin.pelicanTests.activeAccounts', { count: group.active_account_count ?? 0 })}`
  }))
)

const modelOptions = computed(() =>
  modelCandidates.value.map((model) => ({ value: model, label: model }))
)

const canStart = computed(() =>
  selectedGroupID.value !== null && Boolean(modelID.value?.trim())
)

const resultTarget = computed(() => {
  if (!selectedRun.value) return 0
  return Math.max(0, selectedRun.value.total_accounts - selectedRun.value.skipped_count)
})

const progressPercent = computed(() => {
  if (!selectedRun.value) return 0
  if (selectedRun.value.status === 'completed' && resultTarget.value === 0) return 100
  if (resultTarget.value === 0) return 0
  return Math.min(100, Math.round((selectedRun.value.results.length / resultTarget.value) * 100))
})

const previewTitle = computed(() =>
  previewResult.value
    ? t('admin.pelicanTests.previewTitle', { account: previewResult.value.account_name })
    : ''
)

const previewSvg = computed(() =>
  previewResult.value ? safeSvg(previewResult.value) : ''
)

const resultMedia = (result: PelicanTestResult) =>
  Boolean(result.image_url || safeSvg(result))

const deleteMessage = computed(() => {
  const run = pendingDelete.value
  return run
    ? t('admin.pelicanTests.deleteConfirm', {
        group: run.group_name || t('admin.pelicanTests.unknownGroup'),
        model: run.model_id
      })
    : ''
})

const runStatusClass = (status: PelicanTestRunStatus) => {
  if (status === 'running') return 'badge-warning'
  if (status === 'completed') return 'badge-success'
  return 'badge-danger'
}

const resultStatusClass = (status: PelicanTestResultStatus) =>
  status === 'success' ? 'badge-success' : 'badge-danger'

const formatLatency = (milliseconds: number) => {
  if (!Number.isFinite(milliseconds) || milliseconds < 0) return '-'
  if (milliseconds < 1000) return `${milliseconds} ms`
  return `${(milliseconds / 1000).toFixed(2)} s`
}

const safeSvg = (result: PelicanTestResult) => {
  const cached = svgCache.get(result)
  if (cached !== undefined) return cached
  let sanitized = ''
  try {
    sanitized = sanitizeSvg(result.svg_content ?? '')
  } catch (error) {
    console.error('Failed to sanitize pelican test SVG:', error)
  }
  svgCache.set(result, sanitized)
  return sanitized
}

const loadGroups = async () => {
  groupsLoading.value = true
  try {
    groups.value = await adminAPI.groups.getAll()
  } catch (error) {
    console.error('Failed to load pelican test groups:', error)
    appStore.showError(extractApiErrorMessage(error, t('admin.pelicanTests.loadGroupsFailed')))
  } finally {
    groupsLoading.value = false
  }
}

const loadModelCandidates = async (group: AdminGroup) => {
  const requestSeq = ++modelRequestSeq
  modelsLoading.value = true
  modelCandidates.value = []
  try {
    const models = await adminAPI.groups.getModelAllowlistCandidates(group.id, group.platform)
    if (requestSeq !== modelRequestSeq) return
    modelCandidates.value = models
  } catch (error) {
    if (requestSeq !== modelRequestSeq) return
    console.error('Failed to load pelican test models:', error)
    appStore.showError(extractApiErrorMessage(error, t('admin.pelicanTests.loadModelsFailed')))
  } finally {
    if (requestSeq === modelRequestSeq) {
      modelsLoading.value = false
    }
  }
}

const loadRuns = async () => {
  runsLoading.value = true
  try {
    const loaded = await adminAPI.pelicanTests.listRuns({ limit: 50 })
    runs.value = loaded
    if (selectedRunID.value === null || !loaded.some((run) => run.id === selectedRunID.value)) {
      selectedRunID.value = loaded[0]?.id ?? null
    } else if (selectedRun.value) {
      const summary = loaded.find((run) => run.id === selectedRun.value?.id)
      if (summary) selectedRun.value = { ...selectedRun.value, ...summary }
    }
  } catch (error) {
    console.error('Failed to load pelican test history:', error)
    appStore.showError(extractApiErrorMessage(error, t('admin.pelicanTests.loadRunsFailed')))
  } finally {
    runsLoading.value = false
  }
}

const loadRunDetail = async (showError = true) => {
  if (selectedRunID.value === null) {
    selectedRun.value = null
    detailLoading.value = false
    return
  }
  const runID = selectedRunID.value
  const requestSeq = ++detailRequestSeq
  detailLoading.value = true
  try {
    const detail = await adminAPI.pelicanTests.getRun(runID)
    if (requestSeq !== detailRequestSeq || selectedRunID.value !== runID) return
    selectedRun.value = detail
    const index = runs.value.findIndex((run) => run.id === detail.id)
    if (index >= 0) runs.value[index] = { ...runs.value[index], ...detail }
  } catch (error) {
    if (requestSeq !== detailRequestSeq || selectedRunID.value !== runID) return
    console.error('Failed to load pelican test detail:', error)
    if (showError) {
      appStore.showError(extractApiErrorMessage(error, t('admin.pelicanTests.loadRunFailed')))
    }
  } finally {
    if (requestSeq === detailRequestSeq) {
      detailLoading.value = false
    }
  }
}

const stopPolling = () => {
  if (pollingTimer !== null) {
    clearInterval(pollingTimer)
    pollingTimer = null
  }
}

const pollSelectedRun = async () => {
  const runID = selectedRunID.value
  if (runID === null || detailLoading.value) return
  try {
    const detail = await adminAPI.pelicanTests.getRun(runID)
    if (selectedRunID.value !== runID) return
    selectedRun.value = detail
    const index = runs.value.findIndex((run) => run.id === detail.id)
    if (index >= 0) runs.value[index] = { ...runs.value[index], ...detail }
  } catch (error) {
    console.error('Failed to poll pelican test detail:', error)
  }
}

const syncPolling = () => {
  stopPolling()
  if (selectedRun.value?.status === 'running') {
    pollingTimer = setInterval(() => {
      void pollSelectedRun()
    }, 2000)
  }
}

const refreshAll = async () => {
  const runID = selectedRunID.value
  await loadRuns()
  if (runID !== null && selectedRunID.value === runID) {
    await loadRunDetail()
  }
}

const startTest = async () => {
  if (!canStart.value || selectedGroupID.value === null || starting.value) return
  starting.value = true
  try {
    const run = await adminAPI.pelicanTests.createRun({
      group_id: selectedGroupID.value,
      model_id: modelID.value!.trim(),
      prompt: prompt.value.trim() || undefined
    })
    runs.value = [run, ...runs.value.filter((item) => item.id !== run.id)].slice(0, 50)
    selectedRun.value = { ...run, results: [] }
    selectedRunID.value = run.id
    appStore.showSuccess(t('admin.pelicanTests.startSuccess'))
  } catch (error) {
    console.error('Failed to start pelican test:', error)
    appStore.showError(extractApiErrorMessage(error, t('admin.pelicanTests.startFailed')))
  } finally {
    starting.value = false
  }
}

const askDelete = (run: PelicanTestRun) => {
  if (run.status === 'running') return
  pendingDelete.value = run
  deleteDialog.value = true
}

const confirmDelete = async () => {
  const run = pendingDelete.value
  if (!run || deleting.value) return
  deleting.value = true
  try {
    await adminAPI.pelicanTests.deleteRun(run.id)
    const index = runs.value.findIndex((item) => item.id === run.id)
    runs.value = runs.value.filter((item) => item.id !== run.id)
    if (selectedRunID.value === run.id) {
      const nextRun = runs.value[index] ?? runs.value[index - 1] ?? runs.value[0] ?? null
      selectedRunID.value = nextRun?.id ?? null
      selectedRun.value = null
    }
    deleteDialog.value = false
    pendingDelete.value = null
    appStore.showSuccess(t('admin.pelicanTests.deleteSuccess'))
  } catch (error) {
    console.error('Failed to delete pelican test run:', error)
    appStore.showError(extractApiErrorMessage(error, t('admin.pelicanTests.deleteFailed')))
  } finally {
    deleting.value = false
  }
}

const openPreview = (result: PelicanTestResult) => {
  if (!resultMedia(result)) return
  previewResult.value = result
}

watch(selectedGroupID, (groupID) => {
  modelRequestSeq++
  modelCandidates.value = []
  modelID.value = null
  modelsLoading.value = false
  if (groupID === null) return
  const group = groups.value.find((item) => item.id === groupID)
  if (group) void loadModelCandidates(group)
})

watch(selectedRunID, (runID) => {
  detailRequestSeq++
  stopPolling()
  if (runID === null) {
    selectedRun.value = null
    detailLoading.value = false
    return
  }
  if (selectedRun.value?.id !== runID) {
    selectedRun.value = null
  }
  void loadRunDetail()
})

watch(
  () => selectedRun.value?.status,
  () => syncPolling()
)

onMounted(async () => {
  await Promise.all([loadGroups(), loadRuns()])
})

onBeforeUnmount(() => {
  stopPolling()
  detailRequestSeq++
  modelRequestSeq++
})
</script>
