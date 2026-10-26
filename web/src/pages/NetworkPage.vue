<script setup lang="ts">
import { computed, ref } from 'vue'
import { api } from '../api/client'
import { useResource } from '../lib/useResource'
import { formatTime, kindLabels, scopeLabels } from '../lib/format'
import PanelCard from '../components/PanelCard.vue'
import Callout from '../components/Callout.vue'

const net = useResource(() => api.network(), { intervalMs: 10000 })

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

      <Callout tone="info" badge="M2" title="即将提供">
        本机 IPv6 回环检测（<code class="mono">::1</code>）、目标端口 TCP 连通性检测、PF anchor
        与规则命中查询、Tailscale / EasyTier 状态只读查看。
      </Callout>
    </template>
  </div>
</template>
