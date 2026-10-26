<script setup lang="ts">
// OrbStack 容器页面（M6，只读）。
//
// 为什么只读：Docker Socket 的权限等价于 root（能创建特权容器、
// 挂载任意目录）。首版只提供「有哪些容器、在不在跑」的视图，
// 启停操作需要单独的授权与审计设计，不在本阶段。
//
// 不可用是正常状态：用户可能没装 OrbStack，或没启动。

import { computed, h } from 'vue'
import { NTag } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { orbstackApi } from '../api/client'
import { CONTAINER_STATE_META } from '../api/diagnostics'
import type { Container } from '../api/diagnostics'
import { useResource } from '../lib/useResource'
import PanelCard from '../components/PanelCard.vue'
import Callout from '../components/Callout.vue'

const containers = useResource(() => orbstackApi.containers(), { intervalMs: 15000 })

const status = computed(() => containers.data.value?.status)
const items = computed(() => containers.data.value?.items ?? [])
const runningCount = computed(() => items.value.filter((c) => c.state === 'running').length)

const columns = computed<DataTableColumns<Container>>(() => [
  {
    title: '容器',
    key: 'name',
    minWidth: 180,
    render: (row) =>
      h('div', { class: 'min-w-0' }, [
        h('div', { style: 'font-weight:500' }, row.name || row.id),
        h(
          'div',
          { style: 'font-size:11px;color:var(--color-text-tertiary)' },
          row.id,
        ),
      ]),
  },
  {
    title: '镜像',
    key: 'image',
    minWidth: 180,
    ellipsis: { tooltip: true },
  },
  {
    title: '状态',
    key: 'state',
    width: 110,
    render: (row) => {
      const meta = CONTAINER_STATE_META[row.state] ?? {
        label: row.state,
        type: 'default' as const,
      }
      return h(NTag, { size: 'small', type: meta.type, bordered: false }, { default: () => meta.label })
    },
  },
  {
    title: '详情',
    key: 'status',
    minWidth: 160,
    ellipsis: { tooltip: true },
  },
  {
    title: '端口',
    key: 'ports',
    minWidth: 200,
    render: (row) => {
      if (!row.ports?.length) {
        return h('span', { style: 'color:var(--color-text-tertiary)' }, '无')
      }
      return h(
        'div',
        { class: 'flex flex-col' },
        row.ports.map((p) =>
          h('span', { style: 'font-size:11.5px;font-family:var(--n-font-family-mono, monospace)' }, p),
        ),
      )
    },
  },
])
</script>

<template>
  <div class="page-stack">
    <Callout v-if="containers.error.value" tone="danger" badge="错误" title="无法读取容器列表">
      {{ containers.error.value }}
    </Callout>

    <!-- 不可用说明：这是最可能出现的状态，必须讲清原因 -->
    <Callout
      v-if="status && !status.available"
      tone="info"
      badge="不可用"
      title="没有检测到可用的 Docker Socket"
    >
      {{ status.reason }}
      <ul class="m-0 mt-1.5 list-disc pl-4 leading-relaxed">
        <li>确认 OrbStack（或 Docker Desktop）已启动。</li>
        <li>
          预期 Socket 位于 <code class="mono">/var/run/docker.sock</code> 或
          <code class="mono">~/.orbstack/run/docker.sock</code>。
        </li>
        <li>若 Socket 存在但无权限，需让运行 Agent 的用户可读写该文件。</li>
      </ul>
    </Callout>

    <PanelCard v-if="status?.available" title="运行环境" subtitle="Docker API 只读连接">
      <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <div>
          <div class="section-label">状态</div>
          <div class="text-[15px] font-medium">
            <NTag size="small" type="success" :bordered="false">已连接</NTag>
          </div>
        </div>
        <div>
          <div class="section-label">容器</div>
          <div class="text-[15px] font-medium">
            {{ runningCount }} / {{ items.length }} 运行中
          </div>
        </div>
        <div>
          <div class="section-label">API 版本</div>
          <div class="text-[15px] font-medium">{{ status.version || '未知' }}</div>
        </div>
        <div>
          <div class="section-label">Socket</div>
          <div class="mono text-[12px] break-all" style="line-height: 1.6">
            {{ status.socketPath || '—' }}
          </div>
        </div>
      </div>
    </PanelCard>

    <PanelCard v-if="status?.available" title="容器列表" :padded="false">
      <template #actions>
        <NButton size="small" :loading="containers.loading.value" @click="containers.refresh">
          刷新
        </NButton>
      </template>

      <NDataTable
        :columns="columns"
        :data="items"
        :bordered="false"
        :loading="containers.loading.value"
        :row-key="(r: Container) => r.id"
        :max-height="480"
        size="small"
        virtual-scroll
      >
        <template #empty>
          <div class="py-6 text-center text-[12.5px]" style="color: var(--color-text-tertiary)">
            还没有容器
          </div>
        </template>
      </NDataTable>
    </PanelCard>

    <Callout tone="info" badge="只读" title="为什么不能操作容器">
      Docker Socket 的权限等价于 root：通过它可以创建特权容器、挂载宿主机任意目录，
      等于获得整台机器的控制权。首版只提供读取视图，不暴露任何写操作。
      <div class="mt-1 opacity-80">
        容器的启停需要在后续版本中单独设计授权与审计，不能顺手加上——那会让
        「面板的权限」悄悄超过「设计时承诺的权限」。
      </div>
    </Callout>
  </div>
</template>
