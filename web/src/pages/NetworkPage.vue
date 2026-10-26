<script setup lang="ts">
import { computed, ref } from 'vue'
import { useMessage } from 'naive-ui'
import { ApiException, api, diagApi } from '../api/client'
import { groupListeners, OUTCOME_META } from '../api/diagnostics'
import type { ProbeOutcome, TCPProbeResult } from '../api/diagnostics'
import { useResource } from '../lib/useResource'
import { formatTime, kindLabels, scopeLabels } from '../lib/format'
import PanelCard from '../components/PanelCard.vue'
import Callout from '../components/Callout.vue'

const message = useMessage()

const net = useResource(() => api.network(), { intervalMs: 10000 })

// 路由与监听端口：变化不快，30 秒刷新足够。
const diag = useResource(() => diagApi.snapshot(), { intervalMs: 30000 })
const listeners = computed(() => groupListeners(diag.data.value?.ipv6Listeners ?? []))
// 连接表采集失败时，不能把「读不到」显示成「没有监听」。
const hasListenersUnavailable = computed(() =>
  (diag.data.value?.missing ?? []).some((m) => m.metric === 'ipv6Listeners'),
)
const routes = computed(() => diag.data.value?.ipv6Routes ?? [])

// ---- TCP 连通性检测 ----
//
// 默认填 ::1:443：这是「本机 IPv6 回环 + 常用 HTTPS 端口」，
// 一次点击就能验证「服务在不在监听」。
const probeHost = ref('::1')
const probePort = ref<number | null>(443)
const probing = ref(false)
const probeResult = ref<TCPProbeResult | null>(null)

const outcomeMeta = computed(() =>
  probeResult.value ? OUTCOME_META[probeResult.value.outcome as ProbeOutcome] : null,
)

async function runProbe() {
  const port = probePort.value
  if (!probeHost.value.trim() || port == null) {
    message.warning('请填写主机与端口')
    return
  }
  probing.value = true
  probeResult.value = null
  try {
    const res = await diagApi.probe(probeHost.value.trim(), port)
    probeResult.value = res.data
  } catch (e) {
    message.error(e instanceof ApiException ? e.message : String(e))
  } finally {
    probing.value = false
  }
}

/** 从监听列表快速填入探测目标（点端口即可检测） */
function probeFrom(port: number, address: string) {
  probeHost.value = address === '*' ? '::1' : address
  probePort.value = port
  void runProbe()
}

// 默认只显示「活动」接口，避免 utun/anpi 等大量虚拟接口淹没页面。
const onlyActive = ref(true)

const allInterfaces = computed(() => {
  const list = net.data.value?.interfaces ?? []
  return [...list].sort((a, b) => {
    // 回环最后、有地址的靠前、其余按名称
    const score = (i: typeof a) => {
      if (i.loopback) return 2
      if ((i.ipv4?.length ?? 0) + (i.ipv6?.length ?? 0) > 0) return 0
      return 1
    }
    const diff = score(a) - score(b)
    return diff !== 0 ? diff : a.name.localeCompare(b.name)
  })
})

const interfaces = computed(() => {
  if (!onlyActive.value) return allInterfaces.value
  return allInterfaces.value.filter(
    (i) => i.up && (i.ipv4?.length ?? 0) + (i.ipv6?.length ?? 0) > 0,
  )
})

const hiddenCount = computed(() => allInterfaces.value.length - interfaces.value.length)

const v6Summary = computed(() => {
  const d = net.data.value
  if (!d) return null
  if (!d.hasIPv6) return { text: '未检测到 IPv6 地址', type: 'warning' as const }
  if (!d.hasGlobalIPv6) return { text: '仅有链路本地 IPv6，无公网 IPv6', type: 'warning' as const }
  return { text: '已获取公网 IPv6 地址', type: 'success' as const }
})

// 接口类型 → 标签颜色
function kindTagType(kind: string) {
  switch (kind) {
    case 'loopback':
      return 'default' as const
    case 'ethernet':
      return 'info' as const
    case 'wifi':
      return 'info' as const
    case 'tunnel':
      return 'warning' as const
    case 'bridge':
      return 'default' as const
    default:
      return 'default' as const
  }
}

// type="primary" 的 Tag 文字色由 primaryColor 决定，浅色底上只有 3.89:1，
// 且不受 themeOverrides.Tag.textColor 影响，因此用 info（对应 textColorInfo）。
function scopeTagType(scope: string) {
  return scope === 'global' ? ('info' as const) : ('default' as const)
}
</script>

<template>
  <div class="page-stack page-narrow">
    <Callout v-if="net.error.value" tone="danger" badge="错误" title="无法获取网络状态">
      {{ net.error.value }}
    </Callout>

    <Callout v-if="net.degraded.value" tone="warn" badge="降级" title="部分接口数据不可用">
      <ul class="m-0 list-disc space-y-0.5 pl-4">
        <li v-for="n in net.notes.value" :key="n">{{ n }}</li>
      </ul>
    </Callout>

    <template v-if="net.data.value">
      <PanelCard title="IPv6 概况" :subtitle="`采集于 ${formatTime(net.data.value.collectedAt)}`">
        <div class="flex flex-wrap items-center gap-3">
          <NTag v-if="v6Summary" :type="v6Summary.type" :bordered="false" round size="medium">
            {{ v6Summary.text }}
          </NTag>
          <span class="text-[12px]" :style="{ color: 'var(--color-text-secondary)' }">
            共 {{ allInterfaces.length }} 个接口
          </span>
        </div>
        <p class="mt-2 mb-0 text-[11px]" :style="{ color: 'var(--color-text-tertiary)' }">
          本页仅展示接口与地址；连通性检测、路由诊断、PF 规则命中统计将在 M2/M3 提供。
        </p>
      </PanelCard>

      <div class="flex items-center justify-between gap-2">
        <h2 class="section-label m-0">接口列表</h2>
        <!-- 不用 NSwitch 的 checked/unchecked 文字：开关内的白字在轨道色上
             对比度不足（实测 1:1），改用独立 label + 无文字开关。 -->
        <div class="flex items-center gap-2">
          <label
            for="only-active"
            class="cursor-pointer text-[12px]"
            :style="{ color: 'var(--color-text-secondary)' }"
          >
            仅活动接口
            <span v-if="hiddenCount > 0 && onlyActive" :style="{ color: 'var(--color-text-tertiary)' }">
              （已隐藏 {{ hiddenCount }} 个）
            </span>
          </label>
          <NSwitch id="only-active" v-model:value="onlyActive" size="small" />
        </div>
      </div>

      <NEmpty
        v-if="interfaces.length === 0"
        description="没有符合条件的接口，可切换到「全部接口」查看"
        class="py-8"
      />

      <PanelCard v-for="iface in interfaces" :key="iface.name" :padded="false">
        <template #header>
          <div class="flex flex-wrap items-center gap-x-2.5 gap-y-1">
            <span class="mono text-[14px] font-semibold">{{ iface.name }}</span>
            <NTag size="tiny" :bordered="false" :type="kindTagType(iface.kind)">
              {{ kindLabels[iface.kind] ?? iface.kind }}
            </NTag>
            <NTag size="tiny" :bordered="false" :type="iface.up ? 'success' : 'default'">
              {{ iface.up ? '已启用' : '未启用' }}
            </NTag>
            <span class="mono text-[11px]" :style="{ color: 'var(--color-text-tertiary)' }">
              MTU {{ iface.mtu }}
            </span>
            <span
              v-if="iface.hardwareAddr"
              class="mono text-[11px]"
              :style="{ color: 'var(--color-text-tertiary)' }"
            >
              {{ iface.hardwareAddr }}
            </span>
          </div>
        </template>

        <div class="grid gap-4 p-4 sm:grid-cols-2">
          <div>
            <div class="section-label mb-1.5">IPv4</div>
            <ul v-if="iface.ipv4 && iface.ipv4.length > 0" class="m-0 list-none space-y-1 p-0">
              <li
                v-for="a in iface.ipv4"
                :key="a.address"
                class="flex flex-wrap items-baseline gap-2"
              >
                <span class="mono text-[13px]">{{ a.address }}/{{ a.prefixLen }}</span>
                <NTag size="tiny" :bordered="false">{{ scopeLabels[a.scope] ?? a.scope }}</NTag>
              </li>
            </ul>
            <p v-else class="m-0 text-[12px]" :style="{ color: 'var(--color-text-tertiary)' }">
              无
            </p>
          </div>

          <div>
            <div class="section-label mb-1.5">IPv6</div>
            <ul v-if="iface.ipv6 && iface.ipv6.length > 0" class="m-0 list-none space-y-1 p-0">
              <li
                v-for="a in iface.ipv6"
                :key="a.address"
                class="flex flex-wrap items-baseline gap-2"
              >
                <span class="mono cursor-text text-[13px] select-all break-all"
                  >{{ a.address }}/{{ a.prefixLen }}</span
                >
                <NTag size="tiny" :bordered="false" :type="scopeTagType(a.scope)">
                  {{ scopeLabels[a.scope] ?? a.scope }}
                </NTag>
              </li>
            </ul>
            <p v-else class="m-0 text-[12px]" :style="{ color: 'var(--color-text-tertiary)' }">
              无
            </p>
          </div>
        </div>
      </PanelCard>

      <!-- TCP 连通性检测 -->
      <PanelCard title="TCP 连通性检测" subtitle="验证端口是否真的可连接">
        <div class="flex flex-wrap items-end gap-3">
          <div>
            <div class="section-label mb-1">主机</div>
            <NInput
              v-model:value="probeHost"
              placeholder="::1 或 127.0.0.1"
              style="width: 220px"
              @keyup.enter="runProbe"
            />
          </div>
          <div>
            <div class="section-label mb-1">端口</div>
            <NInputNumber
              v-model:value="probePort"
              :min="1"
              :max="65535"
              placeholder="443"
              style="width: 120px"
            />
          </div>
          <NButton type="primary" :loading="probing" @click="runProbe">检测</NButton>
        </div>

        <div v-if="probeResult" class="mt-3">
          <div class="flex flex-wrap items-center gap-2">
            <NTag :type="outcomeMeta?.type ?? 'default'" :bordered="false">
              {{ outcomeMeta?.label ?? probeResult.outcome }}
            </NTag>
            <span class="mono text-[12.5px]">{{ probeResult.target }}</span>
            <span
              v-if="probeResult.latencyMs != null"
              class="text-[12px]"
              :style="{ color: 'var(--color-text-tertiary)' }"
            >
              耗时 {{ probeResult.latencyMs }} ms
            </span>
          </div>
          <div class="mt-1.5 text-[12.5px]" :style="{ color: 'var(--color-text-secondary)' }">
            {{ probeResult.detail }}
          </div>
          <div
            v-if="probeResult.outcome !== 'connected'"
            class="mt-1 text-[12px]"
            :style="{ color: 'var(--color-text-tertiary)' }"
          >
            {{ outcomeMeta?.hint }}
          </div>
        </div>

        <div
          v-else
          class="mt-3 text-[12px]"
          :style="{ color: 'var(--color-text-tertiary)' }"
        >
          只能检测本机地址（回环或本机已配置的地址）。检测外部地址需要显式开启
          <code class="mono">MACPANEL_ALLOW_NONLOCAL_PROBE</code>。
        </div>
      </PanelCard>

      <!-- 监听端口 -->
      <PanelCard title="IPv6 监听端口" :padded="false">
        <template #actions>
          <NButton size="small" :loading="diag.loading.value" @click="diag.refresh">
            刷新
          </NButton>
        </template>

        <Callout
          v-if="diag.data.value?.missing?.length"
          tone="warn"
          badge="部分不可用"
          :title="diag.data.value.missing.map((m) => m.reason).join('；')"
          class="m-3"
        >
          能读到的部分仍显示在下方。
        </Callout>

        <!-- 采集失败时不显示「没有监听」——那是错误结论。
             上方已有的 warning 说明「读不到」，这里不再重复空态文案。 -->
        <div
          v-if="listeners.length === 0 && !hasListenersUnavailable"
          class="px-4 py-6 text-center text-[12.5px]"
          :style="{ color: 'var(--color-text-tertiary)' }"
        >
          没有检测到 IPv6 监听端口
        </div>
        <div v-else class="flex flex-wrap gap-2 px-4 py-3">
          <button
            v-for="l in listeners"
            :key="l.port"
            type="button"
            class="listener-chip"
            :title="`点击检测 ${l.addresses.join(', ')}:${l.port}`"
            @click="probeFrom(l.port, l.addresses[0] ?? '::1')"
          >
            <span class="mono font-medium">{{ l.port }}</span>
            <span class="ml-1 text-[10.5px]" :style="{ color: 'var(--color-text-tertiary)' }">
              {{ l.protocols.join('/') }}
            </span>
          </button>
        </div>
        <div
          v-if="listeners.length > 0"
          class="px-4 pb-3 text-[11.5px]"
          :style="{ color: 'var(--color-text-tertiary)' }"
        >
          点击任一端口可直接检测本机连通性。已建立的连接不计入监听列表。
        </div>
      </PanelCard>

      <!-- 路由表 -->
      <PanelCard title="IPv6 路由表" :padded="false">
        <NDataTable
          :columns="[
            { title: '目标', key: 'destination', minWidth: 200, ellipsis: { tooltip: true } },
            { title: '网关', key: 'gateway', minWidth: 200, ellipsis: { tooltip: true } },
            { title: '接口', key: 'interface', width: 90 },
            { title: '标志', key: 'flags', width: 100 },
          ]"
          :data="routes"
          :bordered="false"
          :loading="diag.loading.value"
          :max-height="300"
          size="small"
          virtual-scroll
        >
          <template #empty>
            <div class="py-6 text-center text-[12.5px]" :style="{ color: 'var(--color-text-tertiary)' }">
              没有 IPv6 路由
            </div>
          </template>
        </NDataTable>
      </PanelCard>

      <Callout tone="info" badge="未实现" title="本阶段未提供">
        PF 规则命中统计、Tailscale / EasyTier 状态查看。这些需要额外的权限与解析逻辑，
        尚未实现——本页只展示已实现的能力，不用占位数据填充。
      </Callout>
    </template>
  </div>
</template>
