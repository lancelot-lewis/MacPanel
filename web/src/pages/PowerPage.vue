<script setup lang="ts">
// 电源与计划任务页面（M3）。
//
// 设计原则（与其他页面一致）：
//  - 所有状态都来自 Agent 的真实响应，不伪造；
//  - 不可用与「值为 0」分开表达（能力缺失时显式说明原因）；
//  - 破坏性操作走二次确认，且把「会发生什么」写清楚。

import { computed, h, onBeforeUnmount, ref, watch } from 'vue'
import { NButton, NTag, useDialog, useMessage } from 'naive-ui'
import type { DataTableColumns, FormInst } from 'naive-ui'
import { ApiException, powerApi, scheduleApi } from '../api/client'
import {
  emptyTask,
  describeRecurrence,
  formatDateTime,
  formatUptime,
  relativeTime,
  RESULT_META,
  WEEKDAY_LABELS,
} from '../api/power'
import type { ExecutionResult, ScheduledTask, TaskExecution } from '../api/power'
import { useResource } from '../lib/useResource'
import PanelCard from '../components/PanelCard.vue'
import Callout from '../components/Callout.vue'

const message = useMessage()
const dialog = useDialog()

// ---- 状态 ----

const status = useResource(() => powerApi.status(), { intervalMs: 15000 })
const tasks = useResource(() => scheduleApi.list())
const executions = useResource(() => scheduleApi.executions(0, 30), { intervalMs: 30000 })

const power = computed(() => status.data.value)
const taskList = computed(() => tasks.data.value?.items ?? [])
const execList = computed(() => executions.data.value?.items ?? [])

// 倒计时用本地秒数滚动，但基准是后端返回的绝对时刻。
// 不用「收到响应时开始数 60 秒」——那样刷新页面就会重置。
const nowTick = ref(Date.now())
const ticker = window.setInterval(() => {
  nowTick.value = Date.now()
}, 1000)
onBeforeUnmount(() => window.clearInterval(ticker))

const pendingCountdown = computed(() => {
  const at = power.value?.pending?.scheduledAt
  if (!at) return null
  const remain = Math.round((new Date(at).getTime() - nowTick.value) / 1000)
  return remain > 0 ? remain : 0
})

const pendingLabel = computed(() => {
  const s = pendingCountdown.value
  if (s == null) return ''
  if (s <= 0) return '即将执行'
  const m = Math.floor(s / 60)
  const sec = s % 60
  return m > 0 ? `剩余 ${m} 分 ${String(sec).padStart(2, '0')} 秒` : `剩余 ${sec} 秒`
})

const canReboot = computed(() => power.value?.canReboot ?? false)
const rebootBlockedReason = computed(() => power.value?.rebootUnavailableReason ?? '')

// ---- 重启控制 ----

const rebootDelay = ref(0) // 分钟
const rebootReason = ref('')
const rebooting = ref(false)

function confirmReboot() {
  const minutes = rebootDelay.value
  const when = minutes === 0 ? '立即' : `${minutes} 分钟后`

  dialog.warning({
    title: '确认重启',
    content: () =>
      h('div', { style: 'font-size:13px;line-height:1.7' }, [
        h('p', { style: 'margin:0 0 8px' }, `系统将在${when}重启。`),
        h(
          'p',
          { style: 'margin:0;color:var(--color-danger)' },
          '正在运行的服务、容器与远程连接都会被中断。如果这台机器只能通过网络访问，请确认你有其它恢复手段。',
        ),
      ]),
    positiveText: minutes === 0 ? '我了解风险，立即重启' : `我了解风险，${minutes} 分钟后重启`,
    negativeText: '取消',
    onPositiveClick: async () => {
      rebooting.value = true
      try {
        await powerApi.reboot(minutes * 60, rebootReason.value)
        message.success(`已排定重启（${when}）`)
        rebootReason.value = ''
        await status.refresh()
      } catch (e) {
        message.error(errText(e))
      } finally {
        rebooting.value = false
      }
    },
  })
}

async function cancelReboot() {
  try {
    await powerApi.cancelReboot()
    message.success('已取消待执行的重启')
    await status.refresh()
  } catch (e) {
    message.error(errText(e))
  }
}

// ---- 任务编辑 ----

const drawerOpen = ref(false)
const editing = ref<ScheduledTask>(emptyTask('Asia/Shanghai'))
const saving = ref(false)
const formRef = ref<FormInst | null>(null)

// 新建任务时用后端报告的时区，避免用户手动输入写错
watch(power, (v) => {
  if (v?.timezone && !v.timezone.includes('/')) {
    // 后端可能回落到缩写（如 CST），那不是一个有效 IANA 名，
    // 此时保持默认值而不是把一个有歧义的名字写进任务。
    return
  }
})

function openCreate() {
  const tz = power.value?.timezone ?? ''
  editing.value = emptyTask(tz.includes('/') ? tz : 'Asia/Shanghai')
  drawerOpen.value = true
}

function openEdit(task: ScheduledTask) {
  // 深拷贝，避免抽屉里的编辑直接污染列表
  editing.value = JSON.parse(JSON.stringify(task)) as ScheduledTask
  drawerOpen.value = true
}

const form = computed(() => editing.value)
const recurrence = computed(() => editing.value.recurrence)

/** 单次任务需要具体的日期时间，用 input[type=datetime-local] 的值转换 */
const onceLocal = computed({
  get() {
    const at = recurrence.value.onceAt
    if (!at) return ''
    const d = new Date(at)
    if (Number.isNaN(d.getTime())) return ''
    const pad = (n: number) => String(n).padStart(2, '0')
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
  },
  set(v: string) {
    if (!v) {
      recurrence.value.onceAt = undefined
      return
    }
    const d = new Date(v)
    if (!Number.isNaN(d.getTime())) {
      recurrence.value.onceAt = d.toISOString()
    }
  },
})

const weekdaysValue = computed(() => recurrence.value.weekdays ?? [])

const rules = {
  name: {
    required: true,
    message: '请输入任务名称',
    trigger: ['blur', 'input'],
  },
  timeOfDay: {
    validator: () => {
      const kind = recurrence.value.kind
      if (kind !== 'daily' && kind !== 'weekly') return true
      const v = recurrence.value.timeOfDay ?? ''
      if (!/^\d{2}:\d{2}$/.test(v)) return new Error('时刻必须是 HH:MM 格式')
      return true
    },
    trigger: ['blur', 'change'],
  },
  weekdays: {
    validator: () => {
      if (recurrence.value.kind !== 'weekly') return true
      if ((recurrence.value.weekdays ?? []).length === 0) return new Error('至少选择一天')
      return true
    },
    trigger: ['change'],
  },
  intervalHours: {
    // 同样不用 required: true —— naive-ui 的 required 对数字输入判定不可靠
    // （实测填了值仍报「必填」），只用自定义 validator。
    validator: () => {
      if (recurrence.value.kind !== 'interval_hours') return true
      const v = recurrence.value.intervalHours
      if (v == null || v < 1) return new Error('间隔必须至少 1 小时')
      if (v > 8760) return new Error('间隔不能超过 8760 小时（一年）')
      return true
    },
    trigger: ['blur', 'change'],
  },
  onceAt: {
    validator: () => {
      if (recurrence.value.kind !== 'once') return true
      if (!recurrence.value.onceAt) return new Error('请选择执行时间')
      if (new Date(recurrence.value.onceAt).getTime() <= Date.now()) {
        return new Error('执行时间必须晚于当前时间')
      }
      return true
    },
    trigger: ['blur', 'change'],
  },
}

async function save() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }

  saving.value = true
  try {
    const task = editing.value
    if (task.id > 0) {
      await scheduleApi.update(task)
      message.success('任务已更新')
    } else {
      await scheduleApi.create(task)
      message.success('任务已创建')
    }
    drawerOpen.value = false
    await Promise.all([tasks.refresh(), status.refresh()])
  } catch (e) {
    message.error(errText(e))
  } finally {
    saving.value = false
  }
}

async function toggleEnabled(task: ScheduledTask) {
  // 启用/禁用走 update，且必须带上完整任务：
  // 后端用整体覆盖语义，缺字段会被当成「清空」。
  const next = { ...task, enabled: !task.enabled }
  try {
    await scheduleApi.update(next)
    message.success(next.enabled ? '任务已启用' : '任务已禁用')
    await tasks.refresh()
  } catch (e) {
    message.error(errText(e))
  }
}

function confirmDelete(task: ScheduledTask) {
  dialog.warning({
    title: '删除任务',
    content: `确定删除「${task.name}」？对应的 launchd 计划会被一并移除，之后不再自动执行。`,
    positiveText: '删除',
    negativeText: '取消',
    onPositiveClick: async () => {
      try {
        await scheduleApi.remove(task.id)
        message.success('任务已删除')
        await tasks.refresh()
      } catch (e) {
        message.error(errText(e))
      }
    },
  })
}

function confirmRunNow(task: ScheduledTask) {
  dialog.warning({
    title: '立即执行',
    content: `「${task.name}」会立即执行（重启机器）。最短间隔与熔断保护仍然生效，可能被跳过。`,
    positiveText: '我了解风险，立即执行',
    negativeText: '取消',
    onPositiveClick: async () => {
      try {
        const res = await scheduleApi.runNow(task.id)
        const result = res.data.result
        if (result === 'skipped') {
          message.warning(`已跳过：${res.data.detail ?? '未满足执行条件'}`)
        } else if (result === 'success') {
          message.success('已提交重启请求')
        } else {
          message.error(res.data.detail ?? '执行失败')
        }
        await Promise.all([tasks.refresh(), executions.refresh(), status.refresh()])
      } catch (e) {
        message.error(errText(e))
      }
    },
  })
}

const syncing = ref(false)
async function syncNow() {
  syncing.value = true
  try {
    const res = await scheduleApi.sync()
    const r = res.data
    const parts: string[] = []
    if (r.installed.length) parts.push(`安装 ${r.installed.length}`)
    if (r.removed.length) parts.push(`移除 ${r.removed.length}`)
    if (r.unchanged.length) parts.push(`无变化 ${r.unchanged.length}`)
    if (r.errors.length) {
      message.error(`同步完成但有错误：${r.errors.join('；')}`)
    } else {
      message.success(`同步完成（${parts.join('，') || '无变更'}）`)
    }
    await tasks.refresh()
  } catch (e) {
    message.error(errText(e))
  } finally {
    syncing.value = false
  }
}

// ---- 表格 ----

const taskColumns = computed<DataTableColumns<ScheduledTask>>(() => [
  {
    title: '名称',
    key: 'name',
    minWidth: 160,
    render: (row) =>
      h('div', { class: 'min-w-0' }, [
        h('div', { style: 'font-weight:500' }, row.name),
        row.disabledReason
          ? h(
              'div',
              { style: 'font-size:11px;color:var(--color-warning-text)' },
              row.disabledReason,
            )
          : null,
      ]),
  },
  {
    title: '执行周期',
    key: 'recurrence',
    minWidth: 150,
    render: (row) => describeRecurrence(row.recurrence),
  },
  {
    title: '下次执行',
    key: 'nextRunAt',
    minWidth: 150,
    render: (row) => {
      if (!row.enabled) return h('span', { style: 'color:var(--color-text-tertiary)' }, '已禁用')
      if (!row.nextRunAt) return '—'
      return h('div', {}, [
        h('div', {}, formatDateTime(row.nextRunAt)),
        h(
          'div',
          { style: 'font-size:11px;color:var(--color-text-tertiary)' },
          relativeTime(row.nextRunAt),
        ),
      ])
    },
  },
  {
    title: '最近结果',
    key: 'lastResult',
    width: 130,
    render: (row) => {
      if (!row.lastResult) {
        return h('span', { style: 'color:var(--color-text-tertiary)' }, '未执行')
      }
      const meta = RESULT_META[row.lastResult as ExecutionResult] ?? {
        label: row.lastResult,
        type: 'default' as const,
      }
      return h(
        'div',
        {},
        [
          h(NTag, { size: 'small', type: meta.type, bordered: false }, { default: () => meta.label }),
          row.lastRunAt
            ? h(
                'div',
                { style: 'font-size:11px;color:var(--color-text-tertiary);margin-top:2px' },
                relativeTime(row.lastRunAt),
              )
            : null,
        ],
      )
    },
  },
  {
    title: '状态',
    key: 'enabled',
    width: 80,
    render: (row) =>
      h(
        NTag,
        { size: 'small', type: row.enabled ? 'success' : 'default', bordered: false },
        { default: () => (row.enabled ? '启用' : '禁用') },
      ),
  },
  {
    title: '操作',
    key: 'actions',
    width: 210,
    render: (row) =>
      h('div', { class: 'flex gap-1' }, [
        h(
          NButton,
          { size: 'tiny', quaternary: true, onClick: () => openEdit(row) },
          { default: () => '编辑' },
        ),
        h(
          NButton,
          { size: 'tiny', quaternary: true, onClick: () => void toggleEnabled(row) },
          { default: () => (row.enabled ? '禁用' : '启用') },
        ),
        h(
          NButton,
          { size: 'tiny', quaternary: true, onClick: () => confirmRunNow(row) },
          { default: () => '立即执行' },
        ),
        h(
          NButton,
          { size: 'tiny', quaternary: true, type: 'error', onClick: () => confirmDelete(row) },
          { default: () => '删除' },
        ),
      ]),
  },
])

const execColumns = computed<DataTableColumns<TaskExecution>>(() => [
  { title: '时间', key: 'startedAt', width: 150, render: (r) => formatDateTime(r.startedAt) },
  { title: '任务', key: 'taskName', minWidth: 140, render: (r) => r.taskName || `#${r.taskId}` },
  {
    title: '结果',
    key: 'result',
    width: 90,
    render: (r) => {
      const meta = RESULT_META[r.result] ?? { label: r.result, type: 'default' as const }
      return h(NTag, { size: 'small', type: meta.type, bordered: false }, { default: () => meta.label })
    },
  },
  {
    title: '说明',
    key: 'detail',
    minWidth: 240,
    ellipsis: { tooltip: true },
    render: (r) => r.detail || '—',
  },
])

// ---- 工具 ----

function errText(e: unknown): string {
  if (e instanceof ApiException) return e.message
  return e instanceof Error ? e.message : String(e)
}
</script>

<template>
  <div class="page-stack">
    <Callout v-if="status.error.value" tone="danger" badge="错误" title="无法获取电源状态">
      {{ status.error.value }}
    </Callout>

    <!-- 待执行重启横幅 -->
    <Callout v-if="power?.pending" tone="warn" badge="待执行" title="重启已排定">
      <div class="flex flex-wrap items-center gap-x-4 gap-y-1">
        <span>
          计划时刻 <b>{{ formatDateTime(power.pending.scheduledAt) }}</b>
          <span style="color: var(--color-danger); font-weight: 600">（{{ pendingLabel }}）</span>
        </span>
        <span>原因：{{ power.pending.reason || '未填写' }}</span>
        <span>发起者：{{ power.pending.requestedBy || '未知' }}</span>
        <NButton size="tiny" type="error" secondary @click="cancelReboot">取消重启</NButton>
      </div>
    </Callout>

    <!-- 能力缺失说明：不能让用户以为「点不了」是 bug -->
    <Callout v-if="power && !canReboot" tone="info" badge="受限" title="当前无法执行重启">
      {{ rebootBlockedReason || '当前进程不具备重启权限。' }}
      <div class="mt-1 opacity-80">
        计划任务仍可创建并显示在列表中，但真正执行时会被跳过并记录原因。
      </div>
    </Callout>

    <!-- 重启控制 -->
    <PanelCard title="立即重启" subtitle="破坏性操作，会中断所有服务与连接">
      <div class="flex flex-wrap items-end gap-3">
        <div>
          <div class="section-label mb-1">执行时间</div>
          <NSelect
            v-model:value="rebootDelay"
            style="width: 150px"
            :options="[
              { label: '立即', value: 0 },
              { label: '1 分钟后', value: 1 },
              { label: '5 分钟后', value: 5 },
              { label: '30 分钟后', value: 30 },
            ]"
          />
        </div>
        <div class="min-w-[200px] flex-1">
          <div class="section-label mb-1">原因（可选）</div>
          <NInput
            v-model:value="rebootReason"
            placeholder="例如：系统更新后重启"
            :maxlength="200"
          />
        </div>
        <NButton
          type="error"
          :disabled="!canReboot || !!power?.pending"
          :loading="rebooting"
          @click="confirmReboot"
        >
          重启
        </NButton>
      </div>
      <div v-if="power?.pending" class="mt-2 text-[11.5px]" style="color: var(--color-text-tertiary)">
        已有待执行的重启，取消后才能排定新的。
      </div>
    </PanelCard>

    <!-- 运行状态 -->
    <PanelCard title="运行状态" subtitle="来自 Agent 的真实读数">
      <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <div>
          <div class="section-label">已运行</div>
          <div class="text-[15px] font-medium">{{ formatUptime(power?.uptimeSeconds ?? 0) }}</div>
        </div>
        <div>
          <div class="section-label">上次启动</div>
          <div class="text-[15px] font-medium">
            {{ power?.lastRebootAt ? formatDateTime(power.lastRebootAt) : '—' }}
          </div>
        </div>
        <div>
          <div class="section-label">系统时区</div>
          <div class="text-[15px] font-medium">{{ power?.timezone || '—' }}</div>
        </div>
        <div>
          <div class="section-label">launchd 调度</div>
          <div class="text-[15px] font-medium">
            <NTag
              size="small"
              :type="power?.scheduleDaemonInstalled ? 'success' : 'default'"
              :bordered="false"
            >
              {{ power?.scheduleDaemonInstalled ? '已安装' : '无任务' }}
            </NTag>
          </div>
        </div>
      </div>
    </PanelCard>

    <!-- 计划任务 -->
    <PanelCard title="计划任务" :padded="false">
      <template #actions>
        <div class="flex gap-2">
          <NButton size="small" :loading="syncing" @click="syncNow">同步到 launchd</NButton>
          <NButton size="small" type="primary" @click="openCreate">新建任务</NButton>
        </div>
      </template>

      <Callout
        v-if="tasks.error.value"
        tone="danger"
        badge="错误"
        title="无法读取计划任务"
        class="m-3"
      >
        {{ tasks.error.value }}
      </Callout>

      <NDataTable
        v-else
        :columns="taskColumns"
        :data="taskList"
        :bordered="false"
        :loading="tasks.loading.value"
        :row-key="(r: ScheduledTask) => r.id"
        size="small"
      >
        <template #empty>
          <div class="py-6 text-center text-[12.5px]" style="color: var(--color-text-tertiary)">
            还没有计划任务。新建后可设定每天或每周自动重启。
          </div>
        </template>
      </NDataTable>
    </PanelCard>

    <Callout tone="info" badge="语义" title="计划任务的行为约定">
      <ul class="m-0 list-disc pl-4 leading-relaxed">
        <li>
          <b>最短间隔 30 分钟</b>：两次执行之间的硬性下限，用于防止配置错误导致循环重启。
        </li>
        <li>
          <b>错过不补执行（默认）</b>：机器关机期间错过的计划会被跳过并记录原因。
          需要在窗口内补执行时，可在任务里显式开启。
        </li>
        <li>
          <b>连续失败 3 次自动禁用</b>：避免环境问题导致反复失败刷日志。
          修好问题后在列表里点「启用」即可恢复，失败计数会清零。
        </li>
        <li>
          <b>调度由 launchd 承担</b>：Agent 停止时任务仍会按时触发，
          因为执行入口是一个独立的命令行进程。
        </li>
      </ul>
    </Callout>

    <!-- 执行历史 -->
    <PanelCard title="执行历史" subtitle="最近 30 条" :padded="false">
      <NDataTable
        :columns="execColumns"
        :data="execList"
        :bordered="false"
        :loading="executions.loading.value"
        :row-key="(r: TaskExecution) => r.id"
        size="small"
        :max-height="360"
        virtual-scroll
      >
        <template #empty>
          <div class="py-6 text-center text-[12.5px]" style="color: var(--color-text-tertiary)">
            暂无执行记录
          </div>
        </template>
      </NDataTable>
    </PanelCard>

    <!-- 任务编辑抽屉 -->
    <NDrawer v-model:show="drawerOpen" :width="480" placement="right">
      <NDrawerContent :title="form.id > 0 ? '编辑任务' : '新建任务'" closable>
        <NForm ref="formRef" :model="form" :rules="rules" label-placement="top" size="small">
          <NFormItem label="任务名称" path="name">
            <NInput v-model:value="form.name" placeholder="例如：每日凌晨重启" :maxlength="64" />
          </NFormItem>

          <NFormItem label="执行周期">
            <NRadioGroup v-model:value="recurrence.kind">
              <NRadioButton value="daily">每天</NRadioButton>
              <NRadioButton value="weekly">每周</NRadioButton>
              <NRadioButton value="interval_hours">固定间隔</NRadioButton>
              <NRadioButton value="once">单次</NRadioButton>
            </NRadioGroup>
          </NFormItem>

          <!-- 每天 / 每周：时刻 -->
          <NFormItem
            v-if="recurrence.kind === 'daily' || recurrence.kind === 'weekly'"
            label="执行时刻"
            path="timeOfDay"
          >
            <NTimePicker
              v-model:formatted-value="recurrence.timeOfDay"
              format="HH:mm"
              value-format="HH:mm"
              :actions="['confirm']"
            />
          </NFormItem>

          <!-- 每周：星期选择 -->
          <NFormItem v-if="recurrence.kind === 'weekly'" label="星期" path="weekdays">
            <NCheckboxGroup
              :value="weekdaysValue"
              @update:value="(v: (string | number)[]) => (recurrence.weekdays = v.map(Number))"
            >
              <div class="flex flex-wrap gap-x-3 gap-y-1">
                <NCheckbox
                  v-for="(label, idx) in WEEKDAY_LABELS"
                  :key="idx"
                  :value="idx"
                  :label="label"
                />
              </div>
            </NCheckboxGroup>
          </NFormItem>

          <!-- 固定间隔 -->
          <NFormItem v-if="recurrence.kind === 'interval_hours'" label="间隔小时数" path="intervalHours">
            <NInputNumber
              v-model:value="recurrence.intervalHours"
              :min="1"
              :max="8760"
              style="width: 180px"
              placeholder="例如：24"
            />
            <span class="ml-2 text-[11.5px]" style="color: var(--color-text-tertiary)">
              以「上次执行时间 + 间隔」计算，不从整点对齐
            </span>
          </NFormItem>

          <!-- 单次 -->
          <NFormItem v-if="recurrence.kind === 'once'" label="执行时间" path="onceAt">
            <NInput v-model:value="onceLocal" type="datetime-local" style="width: 220px" />
            <div class="mt-1 text-[11.5px]" style="color: var(--color-text-tertiary)">
              单次任务不走 launchd，而是由 Agent 持久化倒计时（launchd 无法表达「仅某年某日」）
            </div>
          </NFormItem>

          <NFormItem label="时区">
            <NInput v-model:value="form.timezone" placeholder="Asia/Shanghai" />
            <div class="mt-1 text-[11.5px]" style="color: var(--color-text-tertiary)">
              显式存储：改系统时区不会改变已存在任务的语义
            </div>
          </NFormItem>

          <NFormItem label="最短执行间隔（分钟）">
            <NInputNumber
              :value="Math.round(form.minIntervalSeconds / 60)"
              :min="30"
              :max="525600"
              style="width: 180px"
              @update:value="(v: number | null) => (form.minIntervalSeconds = (v ?? 30) * 60)"
            />
            <div class="mt-1 text-[11.5px]" style="color: var(--color-text-tertiary)">
              不能低于 30 分钟（防循环重启的硬性下限）
            </div>
          </NFormItem>

          <NFormItem label="错过计划时">
            <NSelect
              v-model:value="form.catchUp"
              style="width: 240px"
              :options="[
                { label: '跳过（推荐）', value: 'never' },
                { label: '窗口内补执行', value: 'within_window' },
              ]"
            />
          </NFormItem>

          <NFormItem v-if="form.catchUp === 'within_window'" label="补执行容忍窗口（分钟）">
            <NInputNumber
              :value="Math.round(form.catchUpWindowSeconds / 60)"
              :min="1"
              :max="10080"
              style="width: 180px"
              @update:value="(v: number | null) => (form.catchUpWindowSeconds = (v ?? 360) * 60)"
            />
            <div class="mt-1 text-[11.5px]" style="color: var(--color-text-tertiary)">
              最多 7 天。窗口越大，关机后开机立刻重启的可能性越高
            </div>
          </NFormItem>

          <NFormItem label="启用">
            <NSwitch v-model:value="form.enabled" />
          </NFormItem>
        </NForm>

        <template #footer>
          <div class="flex justify-end gap-2">
            <NButton size="small" @click="drawerOpen = false">取消</NButton>
            <NButton size="small" type="primary" :loading="saving" @click="save">
              {{ form.id > 0 ? '保存' : '创建' }}
            </NButton>
          </div>
        </template>
      </NDrawerContent>
    </NDrawer>
  </div>
</template>
