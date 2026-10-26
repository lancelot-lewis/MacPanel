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

// PF 状态 → NTag 类型
const pfTagType = computed(() => {
  const pf = data.value?.pf
  if (!pf || !pf.readAvailable || pf.enabled == null) return 'default' as const
  return pf.enabled ? 'success' as const : 'default' as const
})

const managedTagType = computed(() => {
  switch (data.value?.managed) {
    case 'configured':
      return 'success' as const
    case 'drift':
      return 'warning' as const
    default:
      return 'default' as const
  }
})

const anchorTagType = computed(() => {
  switch (data.value?.anchor.state) {
    case 'loaded':
      return 'success' as const
    case 'present':
      return 'warning' as const
    default:
      return 'default' as const
  }
})

// 状态对照表：把「期望」「实际」「是否一致」并排列出，避免用户自己拼凑
const statusRows = computed(() => {
  const d = data.value
  if (!d) return []
  return [
    {
      key: 'pf',
      item: 'PF 防火墙',
      expected: d.managed === 'not_configured' ? '不干预' : '由本应用管理',
      actual: pfEnabledText.value,
      consistent: d.pf.readAvailable ? null : false,
      note: d.pf.readAvailable ? '' : '无权限读取，无法判断',
    },
    {
      key: 'anchor',
      item: '本应用 Anchor',
      expected: d.managed === 'not_configured' ? '不创建' : '已加载',
      actual: anchorStateLabels[d.anchor.state] ?? d.anchor.state,
      consistent: d.managed === 'configured' ? d.anchor.state === 'loaded' : null,
      note: d.anchor.present && !d.anchor.referenced ? '文件存在但未被主配置引用' : '',
    },
  ]
})
</script>

<template>
  <div class="page-stack page-narrow">
    <Callout v-if="data && !data.writable" tone="info" badge="只读" title="当前为只读模式">
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
        请勿把下方「不可读」理解为「防火墙已关闭」。要确认真实状态，需以 root 运行 Agent，
        或从终端执行
        <code class="mono rounded px-1 py-0.5 text-[11px]" style="background: rgba(127,127,127,0.15)">
          sudo pfctl -s info
        </code>
        。
      </span>
      <span v-if="data.pf.readError" class="mt-1 block text-[11px] opacity-75">
        原始错误：{{ data.pf.readError }}
      </span>
    </Callout>

    <template v-if="data">
      <!-- 状态对照：期望 vs 实际 -->
      <PanelCard title="状态对照" subtitle="配置期望与实际系统状态的比对">
        <NTable :single-line="false" size="small">
          <thead>
            <tr>
              <th style="width: 30%">项目</th>
              <th style="width: 22%">期望</th>
              <th style="width: 22%">实际</th>
              <th>说明</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in statusRows" :key="row.key">
              <td class="font-medium">{{ row.item }}</td>
              <td :style="{ color: 'var(--color-text-secondary)' }">{{ row.expected }}</td>
              <td>
                <NTag
                  size="small"
                  :bordered="false"
                  :type="row.consistent === true ? 'success' : row.consistent === false ? 'warning' : 'default'"
                >
                  {{ row.actual }}
                </NTag>
              </td>
              <td class="text-[11.5px]" :style="{ color: 'var(--color-text-tertiary)' }">
                {{ row.note || '—' }}
              </td>
            </tr>
          </tbody>
        </NTable>
      </PanelCard>

      <div class="grid gap-4 sm:grid-cols-2">
        <!-- PF -->
        <PanelCard title="PF 状态">
          <div class="flex items-center gap-2">
            <NTag :type="pfTagType" :bordered="false" round size="medium">
              {{ pfEnabledText }}
            </NTag>
          </div>
          <NDescriptions :column="1" label-placement="left" size="small" class="mt-3" :bordered="false">
            <NDescriptionsItem label="读取权限">
              {{ data.pf.readAvailable ? '可读' : '不可读（需 root）' }}
            </NDescriptionsItem>
            <NDescriptionsItem v-if="data.pf.state" label="原始状态">
              <span class="mono">{{ data.pf.state }}</span>
            </NDescriptionsItem>
            <NDescriptionsItem v-if="data.pf.enforceState != null" label="Enforce">
              {{ data.pf.enforceState ? '启用' : '关闭' }}
            </NDescriptionsItem>
            <NDescriptionsItem label="采集时间">
              <span class="mono">{{ formatTime(data.collectedAt) }}</span>
            </NDescriptionsItem>
          </NDescriptions>
        </PanelCard>

        <!-- Anchor -->
        <PanelCard title="本应用 Anchor">
          <div class="flex items-center gap-2">
            <NTag :type="anchorTagType" :bordered="false" round size="medium">
              {{ anchorStateLabels[data.anchor.state] ?? data.anchor.state }}
            </NTag>
          </div>
          <NDescriptions :column="1" label-placement="left" size="small" class="mt-3" :bordered="false">
            <NDescriptionsItem label="名称">
              <span class="mono break-all">{{ data.anchor.name }}</span>
            </NDescriptionsItem>
            <NDescriptionsItem label="规则文件">
              <span class="mono break-all text-[11.5px]">{{ data.anchor.rulePath }}</span>
            </NDescriptionsItem>
            <NDescriptionsItem label="文件存在">
              {{ data.anchor.present ? '是' : '否' }}
            </NDescriptionsItem>
            <NDescriptionsItem label="主配置引用">
              {{ data.anchor.referenced ? '是' : '否' }}
              <NTag
                v-if="!data.anchor.referenced && data.anchor.present"
                size="tiny"
                type="warning"
                :bordered="false"
                class="ml-1"
              >
                未引用则不生效
              </NTag>
            </NDescriptionsItem>
            <NDescriptionsItem v-if="data.anchor.sizeBytes != null" label="文件大小">
              <span class="mono">{{ formatBytes(data.anchor.sizeBytes) }}</span>
            </NDescriptionsItem>
            <NDescriptionsItem v-if="data.anchor.lastModified" label="最后修改">
              <span class="mono">{{ formatTime(data.anchor.lastModified) }}</span>
            </NDescriptionsItem>
            <NDescriptionsItem label="期望状态">
              <NTag :type="managedTagType" size="small" :bordered="false">
                {{ managedStateLabels[data.managed] ?? data.managed }}
              </NTag>
            </NDescriptionsItem>
          </NDescriptions>

          <ul
            v-if="data.anchor.notes && data.anchor.notes.length > 0"
            class="mt-3 mb-0 list-disc space-y-1 pl-4 text-[11px]"
            :style="{ color: 'var(--color-text-tertiary)' }"
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
        <ul class="m-0 list-disc space-y-1.5 pl-4 text-[12.5px]">
          <li v-for="w in data.warnings" :key="w">{{ w }}</li>
        </ul>
      </PanelCard>

      <!-- 已加载 anchor -->
      <PanelCard v-if="data.pf.readAvailable" title="已加载的 Anchor">
        <div v-if="data.loadedAnchors && data.loadedAnchors.length > 0" class="flex flex-wrap gap-1.5">
          <NTag
            v-for="a in data.loadedAnchors"
            :key="a"
            size="small"
            :bordered="false"
            :type="a === data.anchor.name ? 'primary' : 'default'"
            class="mono"
          >
            {{ a }}
          </NTag>
        </div>
        <NEmpty v-else size="small" description="PF 当前没有加载任何 anchor" />
      </PanelCard>

      <!-- M2 计划 -->
      <PanelCard title="即将提供（M2）" subtitle="以下功能在后续里程碑实现，当前不可用">
        <ul
          class="m-0 grid gap-2 pl-4 text-[12.5px] list-disc"
          :style="{ color: 'var(--color-text-secondary)' }"
        >
          <li>IPv6 规则分组、启用/禁用、TCP/UDP、端口或端口范围、来源 CIDR、接口范围</li>
          <li>
            内置服务模板：Caddy（80/443）、sish（2222）、Tailscale（3478/40000）、EasyTier（11010–11013）
          </li>
          <li>规则预览与语法校验、备份、热更新</li>
          <li>应用后 60 秒超时回滚，需显式确认才提交</li>
          <li>配置期望状态与实际 PF 状态的持续比对</li>
        </ul>
      </PanelCard>
    </template>
  </div>
</template>
