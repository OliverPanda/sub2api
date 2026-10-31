<template>
  <div class="table-wrapper">
    <div
      v-if="loading"
      data-testid="channels-loading"
      class="grid grid-cols-1 gap-4 p-4 md:grid-cols-2 xl:grid-cols-3"
    >
      <div
        v-for="index in 6"
        :key="index"
        class="min-h-56 animate-pulse rounded-lg border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800"
      >
        <div class="h-4 w-2/3 rounded bg-gray-200 dark:bg-dark-700" />
        <div class="mt-2 h-3 w-1/2 rounded bg-gray-100 dark:bg-dark-700/70" />
        <div class="mt-6 h-3 w-1/4 rounded bg-gray-100 dark:bg-dark-700/70" />
        <div class="mt-3 flex gap-2">
          <div class="h-7 w-20 rounded bg-gray-100 dark:bg-dark-700/70" />
          <div class="h-7 w-24 rounded bg-gray-100 dark:bg-dark-700/70" />
        </div>
      </div>
    </div>

    <div
      v-else-if="rows.length === 0"
      data-testid="channels-empty"
      class="py-16 text-center"
    >
      <Icon name="inbox" size="xl" class="mx-auto mb-3 h-12 w-12 text-gray-400" />
      <p class="text-sm text-gray-500 dark:text-gray-400">{{ emptyLabel }}</p>
    </div>

    <div
      v-else
      data-testid="available-channel-cards"
      class="grid grid-cols-1 items-stretch gap-4 p-4 md:grid-cols-2 xl:grid-cols-3"
    >
      <article
        v-for="(channel, chIdx) in rows"
        :key="`${channel.name}-${chIdx}`"
        class="min-w-0 rounded-lg border border-gray-200 bg-white p-5 shadow-sm transition-shadow hover:shadow-md dark:border-dark-700 dark:bg-dark-800"
      >
        <header class="mb-4 min-w-0">
          <h2 class="break-words text-base font-semibold text-gray-900 dark:text-white">
            {{ channel.name }}
          </h2>
          <p class="mt-1 break-words text-sm leading-5 text-gray-500 dark:text-gray-400">
            {{ channel.description || '-' }}
          </p>
        </header>

        <div class="divide-y divide-gray-100 dark:divide-dark-700/70">
          <section
            v-for="section in channel.platforms"
            :key="`${channel.name}-${section.platform}`"
            class="min-w-0 py-4 first:pt-0 last:pb-0"
          >
            <span
              :class="[
                'inline-flex items-center gap-1.5 rounded-md border px-2 py-1 text-xs font-medium uppercase',
                platformBadgeClass(section.platform),
              ]"
            >
              <PlatformIcon :platform="section.platform as GroupPlatform" size="xs" />
              {{ section.platform }}
            </span>

            <div class="mt-4">
              <h3 class="mb-2 text-xs font-medium text-gray-500 dark:text-gray-400">
                {{ columns.groups }}
              </h3>
              <div class="flex min-w-0 flex-col gap-2">
                <div
                  v-if="exclusiveGroups(section).length > 0"
                  class="flex min-w-0 flex-wrap items-center gap-1.5"
                >
                  <span
                    class="inline-flex items-center gap-1 text-[10px] font-medium uppercase text-purple-600 dark:text-purple-400"
                    :title="t('availableChannels.exclusiveTooltip')"
                  >
                    <Icon name="shield" size="xs" class="h-3 w-3" />
                    {{ t('availableChannels.exclusive') }}
                  </span>
                  <div
                    v-for="group in exclusiveGroups(section)"
                    :key="`exclusive-${group.id}`"
                    class="inline-flex max-w-full min-w-0 flex-wrap items-center gap-1"
                  >
                    <GroupBadge
                      class="max-w-full"
                      :name="group.name"
                      :platform="group.platform as GroupPlatform"
                      :subscription-type="(group.subscription_type || 'standard') as SubscriptionType"
                      :rate-multiplier="group.rate_multiplier"
                      :user-rate-multiplier="userGroupRates[group.id] ?? null"
                      always-show-rate
                    />
                    <span
                      v-if="hasPeakRate(group)"
                      class="inline-flex items-center gap-1 rounded-md bg-amber-50 px-1.5 py-0.5 text-[10px] font-medium text-amber-700 dark:bg-amber-900/20 dark:text-amber-300"
                      :title="peakRateTitle(group)"
                    >
                      <Icon name="clock" size="xs" class="h-3 w-3" />
                      {{ peakRateLabel(group) }}
                    </span>
                  </div>
                </div>

                <div
                  v-if="publicGroups(section).length > 0"
                  class="flex min-w-0 flex-wrap items-center gap-1.5"
                >
                  <span
                    class="inline-flex items-center gap-1 text-[10px] font-medium uppercase text-gray-500 dark:text-gray-400"
                    :title="t('availableChannels.publicTooltip')"
                  >
                    <Icon name="globe" size="xs" class="h-3 w-3" />
                    {{ t('availableChannels.public') }}
                  </span>
                  <div
                    v-for="group in publicGroups(section)"
                    :key="`public-${group.id}`"
                    class="inline-flex max-w-full min-w-0 flex-wrap items-center gap-1"
                  >
                    <GroupBadge
                      class="max-w-full"
                      :name="group.name"
                      :platform="group.platform as GroupPlatform"
                      :subscription-type="(group.subscription_type || 'standard') as SubscriptionType"
                      :rate-multiplier="group.rate_multiplier"
                      :user-rate-multiplier="userGroupRates[group.id] ?? null"
                      always-show-rate
                    />
                    <span
                      v-if="hasPeakRate(group)"
                      class="inline-flex items-center gap-1 rounded-md bg-amber-50 px-1.5 py-0.5 text-[10px] font-medium text-amber-700 dark:bg-amber-900/20 dark:text-amber-300"
                      :title="peakRateTitle(group)"
                    >
                      <Icon name="clock" size="xs" class="h-3 w-3" />
                      {{ peakRateLabel(group) }}
                    </span>
                  </div>
                </div>
                <span v-if="section.groups.length === 0" class="text-xs text-gray-400">-</span>
              </div>
            </div>

            <div class="mt-4">
              <h3 class="mb-2 text-xs font-medium text-gray-500 dark:text-gray-400">
                {{ columns.supportedModels }}
              </h3>
              <div class="flex min-w-0 flex-wrap gap-1.5">
                <SupportedModelChip
                  v-for="model in section.supported_models"
                  :key="`${section.platform}-${model.name}`"
                  class="max-w-full [&>span]:max-w-full [&>span]:truncate"
                  :model="model"
                  :pricing-key-prefix="pricingKeyPrefix"
                  :no-pricing-label="noPricingLabel"
                  :show-platform="false"
                  :platform-hint="section.platform"
                />
                <span v-if="section.supported_models.length === 0" class="text-xs text-gray-400">
                  {{ noModelsLabel }}
                </span>
              </div>
            </div>
          </section>
        </div>
      </article>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import GroupBadge from '@/components/common/GroupBadge.vue'
import SupportedModelChip from './SupportedModelChip.vue'
import type { UserAvailableChannel, UserAvailableGroup, UserChannelPlatformSection } from '@/api/channels'
import type { GroupPlatform, SubscriptionType } from '@/types'
import { platformBadgeClass } from '@/utils/platformColors'
import { useAppStore } from '@/stores/app'
import { hasPeakRate as groupHasPeakRate, formatPeakRateWindow, serverTimezoneLabel } from '@/utils/peak-rate'

const props = defineProps<{
  columns: {
    name: string
    description: string
    platform: string
    groups: string
    supportedModels: string
  }
  rows: UserAvailableChannel[]
  loading: boolean
  pricingKeyPrefix: string
  noPricingLabel: string
  noModelsLabel: string
  emptyLabel: string
  /** 用户专属倍率（group_id → multiplier）；无专属时由 GroupBadge 仅显示默认倍率。 */
  userGroupRates: Record<number, number>
}>()

// Suppress unused warning — props is accessed via template automatically but
// the explicit reference here keeps the linter from flagging userGroupRates.
void props.userGroupRates

const { t } = useI18n()

function exclusiveGroups(section: UserChannelPlatformSection): UserAvailableGroup[] {
  return section.groups.filter((g) => g.is_exclusive)
}

function publicGroups(section: UserChannelPlatformSection): UserAvailableGroup[] {
  return section.groups.filter((g) => !g.is_exclusive)
}

const appStore = useAppStore()

function hasPeakRate(group: UserAvailableGroup): boolean {
  return groupHasPeakRate(group)
}

function peakRateLabel(group: UserAvailableGroup): string {
  return formatPeakRateWindow(group, serverTimezoneLabel(appStore.cachedPublicSettings?.server_utc_offset))
}

function peakRateTitle(group: UserAvailableGroup): string {
  return t('common.peakRateTooltip', { window: peakRateLabel(group) }) + t('common.peakRateImageNote')
}
</script>
