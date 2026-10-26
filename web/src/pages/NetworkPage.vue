<script setup lang="ts">
import { computed, ref } from 'vue'
import { api } from '../api/client'
import { useResource } from '../lib/useResource'
import { formatTime, kindLabels, scopeLabels } from '../lib/format'
import PanelCard from '../components/PanelCard.vue'
import Callout from '../components/Callout.vue'

const net = useResource(() => api.network(), { intervalMs: 10000 })

// 默认只显示「活动」接口，避免 utun/anpi 等大量虚拟接口淹没页面。
const showAll = ref(false)

const interfaces = computed(() => {
  const list = net.data.value?.interfaces ?? []
  const sorted = [...list].sort((a, b) => {
    // 回环最后、有地址的靠前、其余按名称
    const score = (i: typeof a) => {
      if (i.loopback) return 2
      if ((i.ipv4?.length ?? 0) + (i.ipv6?.length ?? 0) > 0) return 0
      return 1
    }
    const diff = score(a) - score(b)
    return diff !== 0 ? diff : a.name.localeCompare(b.name)
  })
  if (showAll.value) return sorted
  return sorted.filter((i) => i.up && ((i.ipv4?.length ?? 0) + (i.ipv6?.length ?? 0) > 0))
})

const hiddenCount = computed(() => {
  const total = net.data.value?.interfaces?.length ?? 0
  return Math.max(0, total - interfaces.value.length)
})

const v6Summary = computed(() => {
  const d = net.data.value
  if (!d) return null
  if (!d.hasIPv6) return { text: '未检测到 IPv6 地址', tone: 'warn' as const }
  if (!d.hasGlobalIPv6) return { text: '仅有链路本地 IPv6，无公网 IPv6', tone: 'warn' as const }
  return { text: '已获取公网 IPv6 地址', tone: 'ok' as const }
})
</script>

<template>
  <div class="mx-auto flex max-w-5xl flex-col gap-4">
    <Callout v-if="net.error.value" tone="danger" badge="错误" title="无法获取网络状态">
      {{ net.error.value }}
    </Callout>

    <Callout v-if="net.degraded.value" tone="warn" badge="降级" title="部分接口数据不可用">
      <ul class="m-0 list-disc space-y-0.5 pl-4">
        <li v-for="n in net.notes.value" :key="n">{{ n }}</li>
      </ul>
    </Callout>

    <template v-if="net.data.value">
      <PanelCard
        title="IPv6 概况"
        :subtitle="`采集于 ${formatTime(net.data.value.collectedAt)}`"
      >
        <div class="flex flex-wrap items-center gap-3">
          <span
            v-if="v6Summary"
            class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[12px] font-medium"
            :style="{
              background: `color-mix(in oklab, var(--color-${v6Summary.tone === 'ok' ? 'ok' : 'warn'}) 14%, transparent)`,
              color: v6Summary.tone === 'ok' ? 'var(--color-ok)' : 'var(--color-warn)',
            }"
          >
            {{ v6Summary.text }}
          </span>
          <span class="text-[12px] text-[var(--color-text-secondary)]">
            共 {{ net.data.value.interfaces?.length ?? 0 }} 个接口
          </span>
        </div>
        <p class="mt-2 mb-0 text-[11px] text-[var(--color-text-secondary)]">
          本页仅展示接口与地址；连通性检测、路由诊断、PF 规则命中统计将在 M2/M3 提供。
        </p>
      </PanelCard>

      <div class="flex items-center justify-between gap-2">
        <h2 class="m-0 text-[13px] font-semibold text-[var(--color-text-secondary)]">
          接口列表
        </h2>
        <label class="flex cursor-pointer items-center gap-1.5 text-[12px]">
          <input v-model="showAll" type="checkbox" class="size-3.5 accent-[var(--color-accent)]" />
          显示全部接口
          <span v-if="hiddenCount > 0 && !showAll" class="text-[var(--color-text-secondary)]">
            （已隐藏 {{ hiddenCount }} 个）
          </span>
        </label>
      </div>

      <div v-if="interfaces.length === 0" class="card p-5 text-[13px] text-[var(--color-text-secondary)]">
        没有符合条件的接口。勾选「显示全部接口」查看所有接口。
      </div>

      <PanelCard v-for="iface in interfaces" :key="iface.name" :padded="false">
        <div class="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-[var(--color-border-subtle)] px-4 py-2.5">
          <span class="mono text-[14px] font-semibold">{{ iface.name }}</span>
          <span
            class="rounded px-1.5 py-0.5 text-[10px] font-medium"
            style="
              background: color-mix(in oklab, var(--color-text-secondary) 14%, transparent);
            "
          >
            {{ kindLabels[iface.kind] ?? iface.kind }}
          </span>
          <span
            class="inline-flex items-center gap-1 text-[11px]"
            :style="{ color: iface.up ? 'var(--color-ok)' : 'var(--color-text-secondary)' }"
          >
            <span
              class="size-1.5 rounded-full"
              :style="{ background: iface.up ? 'var(--color-ok)' : 'var(--color-text-secondary)' }"
              aria-hidden="true"
            />
            {{ iface.up ? '已启用' : '未启用' }}
          </span>
          <span class="mono text-[11px] text-[var(--color-text-secondary)]">MTU {{ iface.mtu }}</span>
          <span v-if="iface.hardwareAddr" class="mono text-[11px] text-[var(--color-text-secondary)]">
            {{ iface.hardwareAddr }}
          </span>
        </div>

        <div class="grid gap-4 p-4 sm:grid-cols-2">
          <div>
            <div class="card-title mb-1.5">IPv4</div>
            <ul v-if="iface.ipv4 && iface.ipv4.length > 0" class="m-0 list-none space-y-1 p-0">
              <li v-for="a in iface.ipv4" :key="a.address" class="flex flex-wrap items-baseline gap-2">
                <span class="mono text-[13px]">{{ a.address }}/{{ a.prefixLen }}</span>
                <span class="text-[10px] text-[var(--color-text-secondary)]">
                  {{ scopeLabels[a.scope] ?? a.scope }}
                </span>
              </li>
            </ul>
            <p v-else class="m-0 text-[12px] text-[var(--color-text-secondary)]">无</p>
          </div>

          <div>
            <div class="card-title mb-1.5">IPv6</div>
            <ul v-if="iface.ipv6 && iface.ipv6.length > 0" class="m-0 list-none space-y-1 p-0">
              <li v-for="a in iface.ipv6" :key="a.address" class="flex flex-wrap items-baseline gap-2">
                <span class="mono cursor-text text-[13px] select-all break-all"
                  >{{ a.address }}/{{ a.prefixLen }}</span
                >
                <span
                  class="rounded px-1 py-0.5 text-[10px]"
                  :style="{
                    background:
                      a.scope === 'global'
                        ? 'color-mix(in oklab, var(--color-accent) 14%, transparent)'
                        : 'color-mix(in oklab, var(--color-text-secondary) 14%, transparent)',
                    color: a.scope === 'global' ? 'var(--color-accent)' : 'var(--color-text-secondary)',
                  }"
                >
                  {{ scopeLabels[a.scope] ?? a.scope }}
                </span>
              </li>
            </ul>
            <p v-else class="m-0 text-[12px] text-[var(--color-text-secondary)]">无</p>
          </div>
        </div>
      </PanelCard>

      <Callout tone="info" badge="M2" title="即将提供">
        本机 IPv6 回环检测（<code class="mono">::1</code>）、目标端口 TCP 连通性检测、PF anchor 与规则命中查询、
        Tailscale / EasyTier 状态只读查看。
      </Callout>
    </template>
  </div>
</template>
