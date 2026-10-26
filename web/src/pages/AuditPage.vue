<script setup lang="ts">
import { computed, h, ref } from 'vue'
import { NTag } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { api } from '../api/client'
import { useResource } from '../lib/useResource'
import { formatTime } from '../lib/format'
import type { AuditEntry } from '../api/types'
import PanelCard from '../components/PanelCard.vue'
import Callout from '../components/Callout.vue'

const limit = ref(50)
const audit = useResource(() => api.audit(limit.value), { intervalMs: 15000 })

const items = computed(() => audit.data.value?.items ?? [])

const resultMeta: Record<string, { label: string; type: 'success' | 'error' | 'warning' }> = {
  success: { label: '成功', type: 'success' },
  failure: { label: '失败', type: 'error' },
  denied: { label: '拒绝', type: 'warning' },
}

function detailText(detail?: Record<string, unknown>): string {
  if (!detail) return ''
  return Object.entries(detail)
    .map(([k, v]) => `${k}=${String(v)}`)
    .join(' ')
}

// 用 DataTable 而不是手写 table：拿到排序、列宽自适应、空状态与深色适配。
const columns = computed<DataTableColumns<AuditEntry>>(() => [
  {
    title: '时间',
    key: 'at',
    width: 170,
    sorter: (a, b) => new Date(a.at).getTime() - new Date(b.at).getTime(),
    render: (row) => h('span', { class: 'mono text-[12px]' }, formatTime(row.at)),
  },
  {
    title: '动作',
    key: 'action',
    width: 170,
    render: (row) => h('span', { class: 'mono text-[12px] font-medium' }, row.action),
  },
  {
    title: '调用方',
    key: 'actor',
    width: 180,
    ellipsis: { tooltip: true },
    render: (row) => h('span', { class: 'mono text-[11.5px] opacity-80' }, row.actor),
  },
  {
    title: '结果',
    key: 'result',
    width: 90,
    filterOptions: [
      { label: '成功', value: 'success' },
      { label: '失败', value: 'failure' },
      { label: '拒绝', value: 'denied' },
    ],
    filter: (value, row) => row.result === value,
    // 用 NTag 而非自绘 span：Tag 的文字色已在主题里压深到满足 WCAG AA，
    // 自绘要用 accent 色时只有 4.0:1，容易踩坑。
    render: (row) => {
      const m = resultMeta[row.result] ?? { label: row.result, type: 'warning' as const }
      return h(
        NTag,
        { size: 'small', bordered: false, type: m.type },
        { default: () => m.label },
      )
    },
  },
  {
    title: '补充',
    key: 'detail',
    ellipsis: { tooltip: true },
    render: (row) => {
      const text = [row.target, detailText(row.detail)].filter(Boolean).join(' ')
      return h('span', { class: 'mono text-[11px] opacity-70' }, text || '—')
    },
  },
])
</script>

<template>
  <div class="page-stack page-narrow">
    <Callout v-if="audit.error.value" tone="danger" badge="错误" title="无法获取审计日志">
      {{ audit.error.value }}
    </Callout>

    <PanelCard
      title="操作审计"
      subtitle="记录高风险操作与接口调用结果，不包含密码、令牌等敏感内容"
      :padded="false"
    >
      <template #actions>
        <NSelect
          v-model:value="limit"
          size="small"
          style="width: 120px"
          :options="[
            { label: '最近 50 条', value: 50 },
            { label: '最近 100 条', value: 100 },
            { label: '最近 200 条', value: 200 },
          ]"
          @update:value="audit.refresh()"
        />
      </template>

      <NDataTable
        :columns="columns"
        :data="items"
        :loading="audit.loading.value"
        :row-key="(row: AuditEntry) => row.id"
        :bordered="false"
        :single-line="false"
        size="small"
        :pagination="{ pageSize: 20, showSizePicker: false }"
        :scroll-x="760"
      >
        <template #empty>
          <NEmpty description="暂无审计记录，访问其他页面后会自动产生" class="py-6" />
        </template>
      </NDataTable>
    </PanelCard>

    <Callout tone="info" badge="说明">
      审计日志保存在本机 SQLite 数据库中。M2 起，防火墙规则应用、回滚、定时重启等高风险操作
      会记录完整的前后状态与操作者身份。
    </Callout>
  </div>
</template>
