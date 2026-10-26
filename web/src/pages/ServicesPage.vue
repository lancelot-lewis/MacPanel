<script setup lang="ts">
// 启动项管理页面（M4）。
//
// 核心约束来自 PLAN.md 5.4：
//  - 系统关键服务只读；第三方服务默认只读；禁止任意路径操作；
//  - 只对「明确受管」的服务提供启停；
//  - 不能因为操作把管理面板自己关掉。
//
// 因此界面分三层呈现：系统关键（灰）、本应用（标记只读）、
// 第三方（可登记）。开关只在「已登记 + 已授权」时出现。

import { computed, h, ref } from 'vue'
import { NButton, NTag, useDialog, useMessage } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { ApiException, servicesApi } from '../api/client'
import { ACTION_LABELS, KIND_META } from '../api/services'
import type { ServiceAction, ServiceDetail, ServiceItem } from '../api/services'
import { useResource } from '../lib/useResource'
import PanelCard from '../components/PanelCard.vue'
import Callout from '../components/Callout.vue'

const message = useMessage()
const dialog = useDialog()

// ---- 数据 ----

const includeGui = ref(true)
const overview = useResource(() => servicesApi.list(includeGui.value), { intervalMs: 20000 })

const allItems = computed(() => overview.data.value?.items ?? [])
const summary = computed(() => overview.data.value?.summary)

// 筛选：默认只看第三方（系统服务有几百条，混在一起没法用）
type Filter = 'third' | 'managed' | 'running' | 'all'
const filter = ref<Filter>('third')
const keyword = ref('')

const items = computed(() => {
  let list = allItems.value
  switch (filter.value) {
    case 'third':
      list = list.filter((s) => s.kind === 'third_party')
      break
    case 'managed':
      list = list.filter((s) => s.managed)
      break
    case 'running':
      list = list.filter((s) => s.running)
      break
    case 'all':
      break
  }

  const kw = keyword.value.trim().toLowerCase()
  if (kw) {
    list = list.filter(
      (s) =>
        s.label.toLowerCase().includes(kw) ||
        (s.displayName ?? '').toLowerCase().includes(kw),
    )
  }
  return list
})

const filterOptions = computed(() => [
  { label: `第三方 (${summary.value?.thirdParty ?? 0})`, value: 'third' },
  { label: `已受管 (${summary.value?.managed ?? 0})`, value: 'managed' },
  { label: `运行中 (${summary.value?.running ?? 0})`, value: 'running' },
  { label: `全部 (${summary.value?.total ?? 0})`, value: 'all' },
])

// ---- 详情 ----

const detailOpen = ref(false)
const detailLoading = ref(false)
const detail = ref<ServiceDetail | null>(null)
const detailTarget = ref<ServiceItem | null>(null)

async function openDetail(row: ServiceItem) {
  detailTarget.value = row
  detail.value = null
  detailOpen.value = true
  detailLoading.value = true
  try {
    const res = await servicesApi.detail(row.label, row.domain)
    detail.value = res.data
  } catch (e) {
    message.error(errText(e))
  } finally {
    detailLoading.value = false
  }
}

// ---- 登记 / 授权 ----

const registering = ref(false)

function confirmRegister(row: ServiceItem) {
  dialog.warning({
    title: '登记为受管服务',
    content: () =>
      h('div', { style: 'font-size:13px;line-height:1.7' }, [
        h('p', { style: 'margin:0 0 8px' }, [
          '登记后即可通过面板启动、停止或重启 ',
          h('b', {}, row.label),
          '。',
        ]),
        h(
          'p',
          { style: 'margin:0;color:var(--color-danger)' },
          '如果这个服务是你远程访问这台机器的通道（如 sshd、远程桌面、VPN），停止它会导致失联。请确认你有本地或其它恢复手段。',
        ),
      ]),
    positiveText: '我了解风险，登记并授权',
    negativeText: '取消',
    onPositiveClick: () => doRegister(row, true),
  })
}

async function doRegister(row: ServiceItem, allowControl: boolean) {
  if (!row.plistPath) {
    message.error('该服务没有找到 plist 文件，无法登记（可能是内嵌 plist）')
    return
  }
  registering.value = true
  try {
    await servicesApi.register({
      label: row.label,
      domain: row.domain,
      plistPath: row.plistPath,
      displayName: row.displayName ?? '',
      allowControl,
    })
    message.success(allowControl ? '已登记并授权启停' : '已登记（仅只读）')
    await overview.refresh()
  } catch (e) {
    message.error(errText(e))
  } finally {
    registering.value = false
  }
}

function confirmUnregister(row: ServiceItem) {
  dialog.warning({
    title: '取消登记',
    content: `取消后「${row.label}」将变为只读，无法再通过面板启停。服务本身不受影响。`,
    positiveText: '取消登记',
    negativeText: '返回',
    onPositiveClick: async () => {
      try {
        await servicesApi.unregister(row.label)
        message.success('已取消登记')
        await overview.refresh()
      } catch (e) {
        message.error(errText(e))
      }
    },
  })
}

// ---- 启停 ----

const acting = ref('')

function confirmAction(row: ServiceItem, action: ServiceAction) {
  const label = ACTION_LABELS[action]
  dialog.warning({
    title: `${label}服务`,
    content: () =>
      h('div', { style: 'font-size:13px;line-height:1.7' }, [
        h('p', { style: 'margin:0 0 8px' }, [
          `将对 `,
          h('b', {}, row.label),
          ` 执行「${label}」。`,
        ]),
        h(
          'p',
          { style: 'margin:0;color:var(--color-text-secondary)' },
          '若该服务提供远程访问通道，操作后可能立即失去连接。',
        ),
      ]),
    positiveText: `确认${label}`,
    negativeText: '取消',
    onPositiveClick: () => doAction(row, action),
  })
}

async function doAction(row: ServiceItem, action: ServiceAction) {
  acting.value = row.label
  try {
    const res = await servicesApi.control(row.label, action)
    message.success(res.data.message || `已${ACTION_LABELS[action]}`)
    await overview.refresh()
  } catch (e) {
    message.error(errText(e))
  } finally {
    acting.value = ''
  }
}

// ---- 表格 ----

const columns = computed<DataTableColumns<ServiceItem>>(() => [
  {
    title: '服务',
    key: 'label',
    minWidth: 240,
    render: (row) =>
      h('div', { class: 'min-w-0' }, [
        h('div', { style: 'font-weight:500;word-break:break-all' }, row.displayName || row.label),
        row.displayName
          ? h(
              'div',
              {
                style: 'font-size:11px;color:var(--color-text-tertiary);word-break:break-all',
              },
              row.label,
            )
          : null,
      ]),
  },
  {
    title: '分类',
    key: 'kind',
    width: 100,
    render: (row) => {
      const meta = KIND_META[row.kind]
      return h(NTag, { size: 'small', type: meta.type, bordered: false }, { default: () => meta.label })
    },
  },
  {
    title: '状态',
    key: 'running',
    width: 110,
    render: (row) =>
      row.running
        ? h('div', {}, [
            h(NTag, { size: 'small', type: 'success', bordered: false }, { default: () => '运行中' }),
            h('div', { style: 'font-size:11px;color:var(--color-text-tertiary)' }, `PID ${row.pid}`),
          ])
        : h(NTag, { size: 'small', type: 'default', bordered: false }, { default: () => '已停止' }),
  },
  {
    title: '配置来源',
    key: 'plistPath',
    minWidth: 180,
    ellipsis: { tooltip: true },
    render: (row) =>
      row.plistPath
        ? row.plistPath
        : h('span', { style: 'color:var(--color-text-tertiary)' }, '未找到 plist'),
  },
  {
    title: '操作',
    key: 'actions',
    width: 260,
    render: (row) => {
      const buttons = []

      buttons.push(
        h(
          NButton,
          { size: 'tiny', quaternary: true, onClick: () => void openDetail(row) },
          { default: () => '详情' },
        ),
      )

      if (row.controllable) {
        const busy = acting.value === row.label
        if (row.running) {
          buttons.push(
            h(
              NButton,
              { size: 'tiny', quaternary: true, loading: busy, onClick: () => confirmAction(row, 'restart') },
              { default: () => '重启' },
            ),
            h(
              NButton,
              {
                size: 'tiny',
                quaternary: true,
                type: 'error',
                loading: busy,
                onClick: () => confirmAction(row, 'stop'),
              },
              { default: () => '停止' },
            ),
          )
        } else {
          buttons.push(
            h(
              NButton,
              { size: 'tiny', quaternary: true, loading: busy, onClick: () => confirmAction(row, 'start') },
              { default: () => '启动' },
            ),
          )
        }
        buttons.push(
          h(
            NButton,
            { size: 'tiny', quaternary: true, onClick: () => confirmUnregister(row) },
            { default: () => '取消登记' },
          ),
        )
      } else if (row.kind === 'third_party' && !row.managed) {
        buttons.push(
          h(
            NButton,
            {
              size: 'tiny',
              quaternary: true,
              type: 'primary',
              loading: registering.value,
              disabled: !row.plistPath,
              onClick: () => confirmRegister(row),
            },
            { default: () => '登记' },
          ),
        )
      } else if (row.managed && !row.controllable) {
        // 已登记但未授权
        buttons.push(
          h(
            NButton,
            {
              size: 'tiny',
              quaternary: true,
              onClick: () => void doRegister(row, true),
            },
            { default: () => '授权启停' },
          ),
        )
      }

      return h('div', { class: 'flex flex-wrap gap-1' }, buttons)
    },
  },
])

function errText(e: unknown): string {
  if (e instanceof ApiException) return e.message
  return e instanceof Error ? e.message : String(e)
}
</script>

<template>
  <div class="page-stack">
    <Callout v-if="overview.error.value" tone="danger" badge="错误" title="无法读取服务列表">
      {{ overview.error.value }}
    </Callout>

    <Callout
      v-for="(w, i) in overview.data.value?.warnings ?? []"
      :key="i"
      tone="warn"
      badge="部分不可用"
      :title="w"
    >
      能读到的部分仍显示在下方。缺失的部分不代表「没有服务」。
    </Callout>

    <!-- 统计 -->
    <PanelCard title="概览" subtitle="来自 launchctl 的真实状态">
      <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-5">
        <div>
          <div class="section-label">服务总数</div>
          <div class="text-[15px] font-medium">{{ summary?.total ?? '—' }}</div>
        </div>
        <div>
          <div class="section-label">运行中</div>
          <div class="text-[15px] font-medium">{{ summary?.running ?? '—' }}</div>
        </div>
        <div>
          <div class="section-label">系统关键</div>
          <div class="text-[15px] font-medium">{{ summary?.systemCritical ?? '—' }}</div>
        </div>
        <div>
          <div class="section-label">第三方</div>
          <div class="text-[15px] font-medium">{{ summary?.thirdParty ?? '—' }}</div>
        </div>
        <div>
          <div class="section-label">已受管</div>
          <div class="text-[15px] font-medium">{{ summary?.managed ?? '—' }}</div>
        </div>
      </div>
    </PanelCard>

    <!-- 列表 -->
    <PanelCard title="服务列表" :padded="false">
      <template #actions>
        <div class="flex flex-wrap items-center gap-2">
          <NSelect
            v-model:value="filter"
            size="small"
            style="width: 150px"
            :options="filterOptions"
          />
          <NInput
            v-model:value="keyword"
            size="small"
            placeholder="筛选标签"
            clearable
            style="width: 160px"
          />
          <NButton size="small" :loading="overview.loading.value" @click="overview.refresh">
            刷新
          </NButton>
        </div>
      </template>

      <NDataTable
        :columns="columns"
        :data="items"
        :bordered="false"
        :loading="overview.loading.value"
        :row-key="(r: ServiceItem) => r.domain + '/' + r.label"
        :max-height="520"
        size="small"
        virtual-scroll
      >
        <template #empty>
          <div class="py-6 text-center text-[12.5px]" style="color: var(--color-text-tertiary)">
            当前筛选条件下没有服务
          </div>
        </template>
      </NDataTable>
    </PanelCard>

    <Callout tone="info" badge="规则" title="为什么大部分服务是只读的">
      <ul class="m-0 list-disc pl-4 leading-relaxed">
        <li>
          <b>系统关键服务</b>（com.apple.* 等）永久只读：停止它们的后果不可预测，
          可能是网络栈或文件系统守护进程。
        </li>
        <li>
          <b>本应用自身</b>永久只读：停掉 Agent 等于切断面板的数据来源，
          而你很可能正通过它访问这台机器。需要停请走 CLI 或 launchctl。
        </li>
        <li>
          <b>第三方服务</b>默认只读，需显式登记并授权。登记时会记录 plist 路径，
          后续启停只用这条登记过的路径——不接受任意路径，否则等于开放任意程序执行。
        </li>
      </ul>
    </Callout>

    <!-- 详情抽屉 -->
    <NDrawer v-model:show="detailOpen" :width="520" placement="right">
      <NDrawerContent :title="`服务详情 · ${detailTarget?.label ?? ''}`" closable>
        <NSpin :show="detailLoading">
          <div v-if="detail" class="flex flex-col gap-4">
            <NDescriptions :column="1" size="small" bordered>
              <NDescriptionsItem label="标签">
                <span style="word-break: break-all">{{ detail.label }}</span>
              </NDescriptionsItem>
              <NDescriptionsItem label="域">
                {{ detail.domain === 'system' ? '系统域（LaunchDaemons）' : '用户域（LaunchAgents）' }}
              </NDescriptionsItem>
              <NDescriptionsItem label="配置文件">
                <span style="word-break: break-all">{{ detail.plistPath || '未找到' }}</span>
              </NDescriptionsItem>
              <NDescriptionsItem v-if="detail.info?.programArguments?.length" label="启动命令">
                <code style="word-break: break-all; font-size: 11.5px">
                  {{ detail.info.programArguments.join(' ') }}
                </code>
              </NDescriptionsItem>
              <NDescriptionsItem v-else-if="detail.info?.program" label="可执行文件">
                <code style="word-break: break-all; font-size: 11.5px">
                  {{ detail.info.program }}
                </code>
              </NDescriptionsItem>
              <NDescriptionsItem v-if="detail.info" label="加载时启动">
                {{ detail.info.runAtLoad ? '是' : '否' }}
              </NDescriptionsItem>
              <NDescriptionsItem v-if="detail.info" label="保持运行">
                {{ detail.info.keepAlive ? '是' : '否' }}
              </NDescriptionsItem>
            </NDescriptions>

            <div>
              <div class="section-label mb-1">日志入口</div>
              <div v-if="detail.logPaths?.length" class="flex flex-col gap-1">
                <code
                  v-for="p in detail.logPaths"
                  :key="p"
                  style="font-size: 11.5px; word-break: break-all"
                >
                  {{ p }}
                </code>
              </div>
              <div v-else class="text-[12px]" style="color: var(--color-text-secondary)">
                该服务未在 plist 中声明日志文件。可用系统统一日志查询：
                <div class="mt-1">
                  <code style="font-size: 11.5px; word-break: break-all">
                    {{ detail.journalHint }}
                  </code>
                </div>
              </div>
            </div>

            <div v-if="detail.managed">
              <div class="section-label mb-1">受管状态</div>
              <NTag :type="detail.managed.allowControl ? 'success' : 'warning'" size="small">
                {{ detail.managed.allowControl ? '已授权启停' : '已登记（只读）' }}
              </NTag>
            </div>
            <div v-else>
              <div class="section-label mb-1">受管状态</div>
              <NTag size="small">未登记</NTag>
            </div>
          </div>
        </NSpin>
      </NDrawerContent>
    </NDrawer>
  </div>
</template>
