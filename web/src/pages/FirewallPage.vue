<script setup lang="ts">
import { computed } from 'vue'
import { api } from '../api/client'
import { useResource } from '../lib/useResource'
import {
  anchorStateLabels,
  formatBytes,
  formatTime,
  managedStateLabels,
} from '../lib/format'
import PanelCard from '../components/PanelCard.vue'
import Callout from '../components/Callout.vue'

// 防火墙状态变化不频繁，10 秒刷新即可。
const fw = useResource(() => api.firewallStatus(), { intervalMs: 10000 })

const data = computed(() => fw.data.value)

const pfEnabledText = computed(() => {
  const pf = data.value?.pf
  if (!pf) return '—'
  if (!pf.readAvailable) return '不可读'
  if (pf.enabled == null) return '未知'
  return pf.enabled ? '已启用' : '已关闭'
})

const pfEnabledTone = computed(() => {
  const pf = data.value?.pf
  if (!pf || !pf.readAvailable || pf.enabled == null) return 'muted'
  return pf.enabled ? 'ok' : 'muted'
})

const managedTone = computed(() => {
  switch (data.value?.managed) {
    case 'configured':
      return 'ok'
    case 'drift':
      return 'warn'
    default:
      return 'muted'
  }
})

const anchorTone = computed(() => {
  switch (data.value?.anchor.state) {
    case 'loaded':
      return 'ok'
    case 'present':
      return 'warn'
    default:
      return 'muted'
  }
})

function toneColor(tone: string): string {
  switch (tone) {
    case 'ok':
      return 'var(--color-ok)'
    case 'warn':
      return 'var(--color-warn)'
    case 'danger':
      return 'var(--color-danger)'
    default:
      return 'var(--color-text-secondary)'
  }
}
</script>

<template>
  <div class="mx-auto flex max-w-5xl flex-col gap-4">
    <Callout
      v-if="data && !data.writable"
      tone="info"
      badge="只读"
      title="当前为只读模式"
    >
      M1 阶段尚未实现规则写入。规则编辑、语法校验、应用、超时回滚等功能将在 M2 提供，
      后端不会把未实现的写操作伪装成成功。
    </Callout>

    <Callout v-if="fw.error.value" tone="danger" badge="错误" title="无法获取防火墙状态">
      {{ fw.error.value }}
    </Callout>

    <!-- 关键警告：PF 不可读 -->
    <Callout
      v-if="data && !data.pf.readAvailable"
      tone="warn"
      badge="注意"
      title="无法确认 PF 运行状态"
    >
      {{ data.pf.detail || '当前进程无权限读取 PF 状态。' }}
      <span class="mt-1 block">
        请勿把下方「不可读」理解为「防火墙已关闭」。要确认真实状态，需以
        root 运行 Agent，或从终端执行
        <code class="mono rounded bg-black/10 px-1 py-0.5 text-[11px] dark:bg-white/10"
          >sudo pfctl -s info</code
        >。
      </span>
      <span v-if="data.pf.readError" class="mt-1 block text-[11px] opacity-75">
        原始错误：{{ data.pf.readError }}
      </span>
    </Callout>

    <template v-if="data">
      <div class="grid gap-4 sm:grid-cols-2">
        <PanelCard title="PF 状态">
          <div class="flex items-center gap-2">
            <span
              class="size-2 rounded-full"
              :style="{ background: toneColor(pfEnabledTone) }"
              aria-hidden="true"
            />
            <span class="text-lg font-semibold">{{ pfEnabledText }}</span>
          </div>
          <dl class="mt-3 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-[12px]">
            <dt class="text-[var(--color-text-secondary)]">读取权限</dt>
            <dd class="m-0">{{ data.pf.readAvailable ? '可读' : '不可读（需 root）' }}</dd>
            <template v-if="data.pf.state">
              <dt class="text-[var(--color-text-secondary)]">原始状态</dt>
              <dd class="mono m-0">{{ data.pf.state }}</dd>
            </template>
            <template v-if="data.pf.enforceState != null">
              <dt class="text-[var(--color-text-secondary)]">Enforce</dt>
              <dd class="m-0">{{ data.pf.enforceState ? '启用' : '关闭' }}</dd>
            </template>
            <dt class="text-[var(--color-text-secondary)]">采集时间</dt>
            <dd class="mono m-0">{{ formatTime(data.collectedAt) }}</dd>
          </dl>
        </PanelCard>

        <PanelCard title="本应用 Anchor">
          <div class="flex items-center gap-2">
            <span
              class="size-2 rounded-full"
              :style="{ background: toneColor(anchorTone) }"
              aria-hidden="true"
            />
            <span class="text-lg font-semibold">{{
              anchorStateLabels[data.anchor.state] ?? data.anchor.state
            }}</span>
          </div>
          <dl class="mt-3 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-[12px]">
            <dt class="text-[var(--color-text-secondary)]">名称</dt>
            <dd class="mono m-0 break-all">{{ data.anchor.name }}</dd>
            <dt class="text-[var(--color-text-secondary)]">规则文件</dt>
            <dd class="mono m-0 break-all">{{ data.anchor.rulePath }}</dd>
            <dt class="text-[var(--color-text-secondary)]">文件存在</dt>
            <dd class="m-0">{{ data.anchor.present ? '是' : '否' }}</dd>
            <dt class="text-[var(--color-text-secondary)]">主配置引用</dt>
            <dd class="m-0">
              {{ data.anchor.referenced ? '是' : '否' }}
              <span
                v-if="!data.anchor.referenced && data.anchor.present"
                class="ml-1 text-[var(--color-warn)]"
                >（未引用则规则不会生效）</span
              >
            </dd>
            <template v-if="data.anchor.sizeBytes != null">
              <dt class="text-[var(--color-text-secondary)]">文件大小</dt>
              <dd class="mono m-0">{{ formatBytes(data.anchor.sizeBytes) }}</dd>
            </template>
            <template v-if="data.anchor.lastModified">
              <dt class="text-[var(--color-text-secondary)]">最后修改</dt>
              <dd class="mono m-0">{{ formatTime(data.anchor.lastModified) }}</dd>
            </template>
            <dt class="text-[var(--color-text-secondary)]">期望状态</dt>
            <dd class="m-0" :style="{ color: toneColor(managedTone) }">
              {{ managedStateLabels[data.managed] ?? data.managed }}
            </dd>
          </dl>

          <ul
            v-if="data.anchor.notes && data.anchor.notes.length > 0"
            class="mt-3 mb-0 list-disc space-y-1 pl-4 text-[11px] text-[var(--color-text-secondary)]"
          >
            <li v-for="note in data.anchor.notes" :key="note">{{ note }}</li>
          </ul>
        </PanelCard>
      </div>

      <!-- 警告清单 -->
      <PanelCard
        v-if="data.warnings && data.warnings.length > 0"
        title="诊断提示"
        subtitle="以下为系统自动检测到的与期望不一致之处"
      >
        <ul class="m-0 list-disc space-y-1.5 pl-4 text-[12px]">
          <li v-for="w in data.warnings" :key="w">{{ w }}</li>
        </ul>
      </PanelCard>

      <!-- 已加载 anchor 列表 -->
      <PanelCard v-if="data.pf.readAvailable" title="已加载的 Anchor">
        <div v-if="data.loadedAnchors && data.loadedAnchors.length > 0" class="flex flex-wrap gap-1.5">
          <span
            v-for="a in data.loadedAnchors"
            :key="a"
            class="mono rounded px-2 py-0.5 text-[11px]"
            :style="{
              background:
                a === data.anchor.name
                  ? 'color-mix(in oklab, var(--color-accent) 18%, transparent)'
                  : 'color-mix(in oklab, var(--color-text-secondary) 12%, transparent)',
              color: a === data.anchor.name ? 'var(--color-accent)' : 'inherit',
            }"
          >
            {{ a }}
          </span>
        </div>
        <p v-else class="m-0 text-[13px] text-[var(--color-text-secondary)]">
          PF 当前没有加载任何 anchor。
        </p>
      </PanelCard>

      <!-- 计划中的能力 -->
      <PanelCard title="即将提供（M2）" subtitle="以下功能在后续里程碑实现，当前不可用">
        <ul class="m-0 grid gap-2 pl-4 text-[12px] text-[var(--color-text-secondary)] list-disc">
          <li>IPV6 规则分组、启用/禁用、TCP/UDP、端口或端口范围、来源 CIDR、接口范围</li>
          <li>内置服务模板：Caddy（80/443）、sish（2222）、Tailscale（3478/40000）、EasyTier（11010–11013）</li>
          <li>规则预览与语法校验、备份、热更新</li>
          <li>应用后 60 秒超时回滚，需显式确认才提交</li>
          <li>配置期望状态与实际 PF 状态的持续比对</li>
        </ul>
      </PanelCard>
    </template>
  </div>
</template>
