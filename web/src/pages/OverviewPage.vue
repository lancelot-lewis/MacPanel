<script setup lang="ts">
import { computed } from 'vue'
import { api } from '../api/client'
import { useResource } from '../lib/useResource'
import {
  formatBytes,
  formatDuration,
  formatNumber,
  formatPercent,
  formatTime,
} from '../lib/format'
import PanelCard from '../components/PanelCard.vue'
import StatBlock from '../components/StatBlock.vue'
import Callout from '../components/Callout.vue'

// 5 秒刷新实时指标，与设计目标一致。
const overview = useResource(() => api.overview(), { intervalMs: 5000 })

const ov = computed(() => overview.data.value)
const memory = computed(() => ov.value?.memory)

const memUsedRatio = computed(() => {
  const m = memory.value
  if (!m || m.state !== 'ok' || m.usedBytes == null || m.totalBytes <= 0) return null
  return m.usedBytes / m.totalBytes
})

const missingMap = computed(() => {
  const map = new Map<string, string>()
  for (const item of ov.value?.missing ?? []) {
    map.set(item.metric, item.reason)
  }
  return map
})

const isLoadingFirst = computed(() => overview.loading.value && !overview.data.value)

// 内存进度条颜色随占用升高而警示
const memStatus = computed(() => {
  const r = memUsedRatio.value
  if (r == null) return undefined
  if (r > 0.9) return 'error' as const
  if (r > 0.75) return 'warning' as const
  return 'success' as const
})

function diskStatus(ratio: number) {
  if (ratio > 0.9) return 'error' as const
  if (ratio > 0.8) return 'warning' as const
  return 'default' as const
}
</script>

<template>
  <div class="page-stack page-narrow">
    <!-- 错误提示：保留旧数据但明确标注非最新 -->
    <Callout v-if="overview.error.value" tone="danger" badge="错误" title="无法获取系统状态">
      {{ overview.error.value }}
      <span v-if="overview.lastUpdated.value" class="mt-1 block opacity-80">
        下方显示的是
        {{ formatTime(new Date(overview.lastUpdated.value).toISOString()) }}
        的数据，可能已过期。
      </span>
    </Callout>

    <Callout v-if="overview.degraded.value" tone="warn" badge="降级" title="部分指标不可用">
      <ul class="m-0 list-disc space-y-0.5 pl-4">
        <li v-for="note in overview.notes.value" :key="note">{{ note }}</li>
      </ul>
    </Callout>

    <!-- 首次加载骨架 -->
    <NCard v-if="isLoadingFirst" size="small">
      <NSkeleton text :repeat="2" />
      <NSkeleton text style="width: 60%" />
    </NCard>

    <template v-if="ov">
      <!-- 主机概要 -->
      <PanelCard title="主机" :subtitle="`采集于 ${formatTime(ov.collectedAt)}`">
        <div class="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-4">
          <StatBlock label="主机名" :value="ov.hostname || null" :mono="false" />
          <StatBlock
            label="macOS 版本"
            :value="ov.os.productVersion ? `${ov.os.productVersion} (${ov.os.build})` : null"
            :unavailable="!ov.os.productVersion"
            :unavailable-reason="missingMap.get('os.productVersion')"
            :hint="ov.os.kernel"
          />
          <StatBlock label="架构" :value="ov.os.architecture || null" />
          <StatBlock
            label="运行时长"
            :value="formatDuration(ov.uptimeSeconds)"
            :unavailable="ov.uptimeSeconds == null"
            :unavailable-reason="missingMap.get('bootTime')"
            :hint="ov.bootTime ? `启动于 ${formatTime(ov.bootTime)}` : ''"
          />
        </div>
      </PanelCard>

      <div class="grid gap-4 lg:grid-cols-2">
        <!-- 处理器 -->
        <PanelCard title="处理器">
          <div class="grid grid-cols-2 gap-4">
            <StatBlock
              label="型号"
              :value="ov.cpu.model || null"
              :mono="false"
              :unavailable="!ov.cpu.model"
              :unavailable-reason="missingMap.get('cpu.model')"
            />
            <StatBlock
              label="核心数"
              :value="
                ov.cpu.logicalCores > 0
                  ? `${ov.cpu.logicalCores} 逻辑 / ${ov.cpu.physicalCores} 物理`
                  : null
              "
              :unavailable="ov.cpu.logicalCores <= 0"
            />
          </div>

          <NDivider class="!my-3" />

          <div class="mb-1.5 text-xs" :style="{ color: 'var(--color-text-secondary)' }">
            系统负载（1 / 5 / 15 分钟）
          </div>
          <div v-if="ov.cpu.loadAverage" class="flex items-baseline gap-4">
            <span class="mono text-[19px] font-semibold">{{
              formatNumber(ov.cpu.loadAverage.one)
            }}</span>
            <span class="mono text-[13px]" :style="{ color: 'var(--color-text-secondary)' }">{{
              formatNumber(ov.cpu.loadAverage.five)
            }}</span>
            <span class="mono text-[13px]" :style="{ color: 'var(--color-text-tertiary)' }">{{
              formatNumber(ov.cpu.loadAverage.fifteen)
            }}</span>
          </div>
          <div v-else class="text-[13px]" :style="{ color: 'var(--color-text-secondary)' }">
            不可用
          </div>
        </PanelCard>

        <!-- 内存 -->
        <PanelCard title="内存">
          <template v-if="memory && memory.state === 'ok'">
            <div class="grid grid-cols-2 gap-4">
              <StatBlock
                label="已用"
                :value="formatBytes(memory.usedBytes)"
                :hint="memUsedRatio != null ? formatPercent(memUsedRatio, 1) : ''"
              />
              <StatBlock label="总计" :value="formatBytes(memory.totalBytes)" />
            </div>

            <NProgress
              v-if="memUsedRatio != null"
              class="mt-3"
              type="line"
              :percentage="Math.min(100, Math.max(0, memUsedRatio * 100))"
              :status="memStatus"
              :show-indicator="false"
              :height="8"
              :border-radius="4"
            />

            <p
              class="mt-2 mb-0 text-[11px] leading-relaxed"
              :style="{ color: 'var(--color-text-tertiary)' }"
            >
              可用 {{ formatBytes(memory.availableBytes) }}；已用 = 总量 − 可回收页（free +
              speculative + purgeable + inactive）
            </p>
          </template>
          <div v-else class="text-[13px]" :style="{ color: 'var(--color-text-secondary)' }">
            内存统计不可用
            <span
              v-if="missingMap.get('memory')"
              class="mt-1 block text-[11px] opacity-80"
            >
              原因：{{ missingMap.get('memory') }}
            </span>
          </div>
        </PanelCard>
      </div>

      <!-- 磁盘 -->
      <PanelCard title="磁盘容量">
        <div v-if="ov.disks && ov.disks.length > 0" class="flex flex-col gap-4">
          <div v-for="disk in ov.disks" :key="disk.mountPoint">
            <div class="flex flex-wrap items-baseline justify-between gap-2">
              <div class="min-w-0">
                <span class="mono text-[13px] font-medium">{{ disk.mountPoint }}</span>
                <NTag size="small" :bordered="false" class="ml-2">{{ disk.fileSystem }}</NTag>
              </div>
              <div class="mono text-[12px]" :style="{ color: 'var(--color-text-secondary)' }">
                {{ formatBytes(disk.usedBytes) }} / {{ formatBytes(disk.totalBytes) }}
                <span class="ml-1 font-semibold" :style="{ color: 'var(--color-text-primary)' }">
                  {{ formatPercent(disk.usedRatio) }}
                </span>
              </div>
            </div>
            <NProgress
              class="mt-1.5"
              type="line"
              :percentage="Math.min(100, disk.usedRatio * 100)"
              :status="diskStatus(disk.usedRatio)"
              :show-indicator="false"
              :height="6"
              :border-radius="3"
            />
          </div>
          <p class="m-0 text-[11px]" :style="{ color: 'var(--color-text-tertiary)' }">
            注：APFS 上多个卷共享同一容器，容量数值可能相同，这是系统真实语义。
          </p>
        </div>
        <div v-else class="text-[13px]" :style="{ color: 'var(--color-text-secondary)' }">
          磁盘信息不可用
          <span v-if="missingMap.get('disks')" class="mt-1 block text-[11px] opacity-80">
            原因：{{ missingMap.get('disks') }}
          </span>
        </div>
      </PanelCard>
    </template>

    <Callout tone="info" badge="M1" title="当前阶段说明">
      本版本（M1）仅提供只读状态展示。IPv6 防火墙规则编辑与应用、定时重启、启动项管理均在后续里程碑实现，
      对应接口会明确返回「未实现」，不会伪造操作成功。
    </Callout>
  </div>
</template>
