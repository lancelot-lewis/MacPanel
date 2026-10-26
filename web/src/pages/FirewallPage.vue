<script setup lang="ts">
// IPv6 防火墙：规则编辑 + 预览 + 应用 + 超时确认/回滚。
//
// 这个页面是整个项目风险最高的地方——错误的应用会让机器失联。
// 因此交互上刻意做了几件事：
//  1. 「应用规则」必须经过确认对话框，且对话框里明确写出风险；
//  2. 应用后立刻显示倒计时横幅，确认前不改动其它 UI；
//  3. 生成的 PF 文本始终可见（不只是「预览」按钮才显示），
//     让用户随时能看到「我配的东西最终会长什么样」；
//  4. 强制加入的规则单独列出并解释原因——用户必须知道
//     有些规则不是他写的，但不能删。

import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useDialog, useMessage } from 'naive-ui'
import {
  NAlert,
  NButton,
  NCheckbox,
  NCheckboxGroup,
  NDataTable,
  NEmpty,
  NPopconfirm,
  NSelect,
  NSpace,
  NSpin,
  NTag,
  NTooltip,
} from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { h } from 'vue'
import { firewallApi } from '../api/client'
import { ApiException } from '../api/client'
import type {
  FirewallRule,
  FirewallTemplate,
  PreviewResult,
  RulesGetResult,
  TransactionState,
} from '../api/firewall'
import { api } from '../api/client'
import { useResource } from '../lib/useResource'
import PanelCard from '../components/PanelCard.vue'
import Callout from '../components/Callout.vue'
import TransactionBanner from '../components/TransactionBanner.vue'
import RuleEditor from '../components/RuleEditor.vue'

const message = useMessage()
const dialog = useDialog()

// ---- 数据 ----
const rulesRes = useResource(() => firewallApi.getRules(), { intervalMs: 0 })
const fwStatus = useResource(() => api.firewallStatus(), { intervalMs: 15000 })

const localRules = ref<FirewallRule[]>([])
const denyInterfaces = ref<string[]>([])
const transaction = ref<TransactionState>({ phase: 'idle', revisionId: 0, contentHash: '' })

const preview = ref<PreviewResult | null>(null)
const previewLoading = ref(false)
const saving = ref(false)
const applying = ref(false)
const confirming = ref(false)
const rollingBack = ref(false)

const dirty = ref(false)

const templates = ref<FirewallTemplate[]>([])
const templateSelect = ref<string | null>(null)

const editorShow = ref(false)
const editingRule = ref<FirewallRule | null>(null)

// ---- 从服务器加载后同步到本地可编辑副本 ----
function syncFromServer(data: RulesGetResult) {
  localRules.value = (data.rules ?? []).map((r) => ({ ...r }))
  denyInterfaces.value = [...(data.defaultDenyInterfaces ?? [])]
  transaction.value = data.transaction ?? { phase: 'idle', revisionId: 0, contentHash: '' }
  dirty.value = false
}

// useResource 的 onLoaded 需要在创建时传入，这里改用 watch 兜底
import { watch } from 'vue'
watch(
  () => rulesRes.data.value,
  (d) => {
    if (d) syncFromServer(d)
  },
  { immediate: true },
)

// ---- 接口列表（供规则编辑与默认拒绝选择）----
const knownInterfaces = computed(() => {
  const names = new Set<string>()
  for (const iface of fwStatus.data.value ? [] : []) {
    void iface
  }
  // 从网络快照拿不到时，至少提供常见的物理接口
  for (const n of denyInterfaces.value) names.add(n)
  return Array.from(names)
})

// ---- 预览 ----
async function refreshPreview() {
  previewLoading.value = true
  try {
    const res = await firewallApi.preview({
      rules: localRules.value,
      defaultDenyInterfaces: denyInterfaces.value,
    })
    preview.value = res.data
  } catch (e) {
    message.error(e instanceof ApiException ? e.message : String(e))
    preview.value = null
  } finally {
    previewLoading.value = false
  }
}

// ---- 模板 ----
async function loadTemplates() {
  try {
    const res = await firewallApi.templates()
    templates.value = res.data.items ?? []
  } catch {
    templates.value = []
  }
}

function applyTemplate(key: string | null) {
  if (!key) return
  const tpl = templates.value.find((t) => t.key === key)
  if (!tpl) return

  dialog.info({
    title: `套用模板：${tpl.displayName}`,
    content: () =>
      h('div', { style: 'font-size:13px;line-height:1.7' }, [
        h('p', { style: 'margin:0 0 8px' }, tpl.description),
        tpl.note
          ? h(
              'p',
              { style: 'margin:0 0 8px;color:#c27c00' },
              `⚠ ${tpl.note}`,
            )
          : null,
        h('p', { style: 'margin:0' }, `将添加 ${tpl.rules.length} 条规则（端口需与服务的实际监听核对）。`),
      ]),
    positiveText: '添加',
    negativeText: '取消',
    onPositiveClick: () => {
      let pos = localRules.value.length
      const added = tpl.rules.map((r) => {
        const copy = { ...r, id: 0, position: pos++ }
        return copy
      })
      localRules.value = [...localRules.value, ...added]
      dirty.value = true
      templateSelect.value = null
      message.success(`已添加 ${added.length} 条规则，记得保存并预览`)
      void refreshPreview()
    },
  })
}

// ---- 规则表 ----
const columns = computed<DataTableColumns<FirewallRule>>(() => [
  {
    title: '顺序',
    key: 'position',
    width: 60,
    render: (_row, index) =>
      h('span', { class: 'mono text-[11px] opacity-60' }, String(index + 1)),
  },
  {
    title: '规则',
    key: 'name',
    minWidth: 180,
    render: (row) =>
      h('div', { class: 'min-w-0' }, [
        h(
          'div',
          {
            class: 'truncate text-[12.5px] font-medium',
            style: row.enabled ? '' : 'opacity:0.45;text-decoration:line-through',
          },
          row.name,
        ),
        row.description
          ? h('div', { class: 'truncate text-[11px]', style: 'color:var(--color-text-tertiary)' }, row.description)
          : null,
      ]),
  },
  {
    title: '动作',
    key: 'action',
    width: 80,
    render: (row) =>
      h(
        NTag,
        {
          size: 'tiny',
          bordered: false,
          type: row.action === 'pass' ? 'success' : 'error',
        },
        { default: () => (row.action === 'pass' ? '放行' : '拒绝') },
      ),
  },
  {
    title: '协议 / 端口',
    key: 'protocol',
    width: 130,
    render: (row) => {
      const port =
        row.portFrom == null
          ? '—'
          : row.portTo != null && row.portTo !== row.portFrom
            ? `${row.portFrom}-${row.portTo}`
            : String(row.portFrom)
      return h('span', { class: 'mono text-[11.5px]' }, `${row.protocol.toUpperCase()} ${port}`)
    },
  },
  {
    title: '来源',
    key: 'sourceCidr',
    width: 150,
    ellipsis: { tooltip: true },
    render: (row) =>
      h('span', { class: 'mono text-[11px]' }, row.sourceCidr || 'any'),
  },
  {
    title: '接口',
    key: 'interface',
    width: 80,
    render: (row) => h('span', { class: 'mono text-[11px]' }, row.interface || 'any'),
  },
  {
    title: '启用',
    key: 'enabled',
    width: 70,
    render: (row) =>
      h(NTag, { size: 'tiny', bordered: false, type: row.enabled ? 'success' : 'default' }, {
        default: () => (row.enabled ? '启用' : '禁用'),
      }),
  },
  {
    title: '操作',
    key: 'ops',
    width: 130,
    render: (row, index) =>
      h(NSpace, { size: 4, wrap: false }, {
        default: () => [
          h(
            NButton,
            {
              size: 'tiny',
              quaternary: true,
              onClick: () => {
                editingRule.value = row
                editorShow.value = true
              },
            },
            { default: () => '编辑' },
          ),
          h(
            NButton,
            {
              size: 'tiny',
              quaternary: true,
              disabled: index === 0,
              onClick: () => moveRule(index, -1),
            },
            { default: () => '↑' },
          ),
          h(
            NButton,
            {
              size: 'tiny',
              quaternary: true,
              disabled: index === localRules.value.length - 1,
              onClick: () => moveRule(index, 1),
            },
            { default: () => '↓' },
          ),
          h(
            NPopconfirm,
            {
              onPositiveClick: () => removeRule(index),
            },
            {
              trigger: () => h(NButton, { size: 'tiny', quaternary: true, type: 'error' }, { default: () => '删除' }),
              default: () => '删除这条规则？',
            },
          ),
        ],
      }),
  },
])

function moveRule(index: number, delta: number) {
  const next = index + delta
  if (next < 0 || next >= localRules.value.length) return
  const arr = [...localRules.value]
  const tmp = arr[index]!
  arr[index] = arr[next]!
  arr[next] = tmp
  localRules.value = arr.map((r, i) => ({ ...r, position: i }))
  dirty.value = true
  void refreshPreview()
}

function removeRule(index: number) {
  localRules.value = localRules.value.filter((_, i) => i !== index).map((r, i) => ({ ...r, position: i }))
  dirty.value = true
  void refreshPreview()
}

function onRuleSaved(rule: FirewallRule) {
  if (rule.id > 0) {
    // 编辑已有
    localRules.value = localRules.value.map((r) => (r.id === rule.id ? rule : r))
  } else {
    localRules.value = [...localRules.value, { ...rule, position: localRules.value.length }]
  }
  dirty.value = true
  void refreshPreview()
}

// ---- 保存 ----
async function save() {
  saving.value = true
  try {
    const res = await firewallApi.saveRules({
      rules: localRules.value,
      defaultDenyInterfaces: denyInterfaces.value,
    })
    preview.value = res.data
    dirty.value = false
    message.success('规则已保存到数据库')
  } catch (e) {
    message.error(e instanceof ApiException ? e.message : String(e))
  } finally {
    saving.value = false
  }
}

// ---- 应用 ----
function confirmApply() {
  const ruleCount = localRules.value.filter((r) => r.enabled).length
  const denyText =
    denyInterfaces.value.length > 0
      ? `对 ${denyInterfaces.value.join('、')} 实施默认拒绝入站`
      : '未配置默认拒绝（只添加放行规则，不阻断任何流量）'

  dialog.warning({
    title: '应用防火墙规则',
    content: () =>
      h('div', { style: 'font-size:13px;line-height:1.8' }, [
        h('p', { style: 'margin:0 0 8px' }, `将立即生效 ${ruleCount} 条放行/拒绝规则。`),
        h('p', { style: 'margin:0 0 8px' }, denyText + '。'),
        h(
          'p',
          { style: 'margin:0 0 8px;color:#c27c00;font-weight:600' },
          '这会立即改变系统防火墙状态，可能中断现有连接。',
        ),
        h(
          'p',
          { style: 'margin:0' },
          '应用后有 60 秒确认窗口：期间不确认会自动回滚。请保留一个可用的终端会话。',
        ),
      ]),
    positiveText: '我了解风险，应用',
    negativeText: '取消',
    onPositiveClick: () => {
      void doApply()
    },
  })
}

async function doApply() {
  applying.value = true
  try {
    const res = await firewallApi.apply({
      rules: localRules.value,
      defaultDenyInterfaces: denyInterfaces.value,
    })
    transaction.value = {
      phase: 'pending',
      revisionId: res.data.revisionId,
      contentHash: res.data.contentHash,
      appliedAt: res.data.appliedAt,
      deadline: res.data.deadline,
    }
    if (res.data.warnings?.length) {
      message.warning(res.data.warnings.join('；'))
    } else {
      message.success('规则已应用，请在倒计时结束前确认')
    }
    dirty.value = false
  } catch (e) {
    message.error(e instanceof ApiException ? e.message : String(e))
  } finally {
    applying.value = false
  }
}

// ---- 确认 / 回滚 ----
async function doConfirm() {
  confirming.value = true
  try {
    const res = await firewallApi.confirm()
    transaction.value = res.data
    message.success('配置已确认并保存为可用版本')
    void refreshStatus()
  } catch (e) {
    message.error(e instanceof ApiException ? e.message : String(e))
    void refreshTransaction()
  } finally {
    confirming.value = false
  }
}

async function doRollback(reason = '用户主动回滚') {
  rollingBack.value = true
  try {
    const res = await firewallApi.rollback(reason)
    transaction.value = res.data
    message.success('已回滚到应用前的状态')
    void refreshStatus()
  } catch (e) {
    message.error(e instanceof ApiException ? e.message : String(e))
  } finally {
    rollingBack.value = false
  }
}

async function refreshTransaction() {
  try {
    const res = await firewallApi.transaction()
    transaction.value = res.data.state
  } catch {
    // 读失败不改本地状态：宁可显示旧状态也不要谎报
  }
}

async function refreshStatus() {
  await fwStatus.refresh()
}

// 倒计时归零：不本地推断结果，去问后端
function onExpired() {
  message.warning('确认窗口已结束，正在检查实际状态…')
  void refreshTransaction()
  void refreshStatus()
}

onMounted(async () => {
  await loadTemplates()
  void refreshPreview()
})

// 清理轮询
onBeforeUnmount(() => {
  /* useResource 已在卸载时清理定时器 */
})

// ---- 展示辅助 ----
const phaseText = computed(() => {
  switch (transaction.value.phase) {
    case 'pending':
      return '待确认'
    case 'confirmed':
      return '已确认'
    case 'rolled_back':
      return '已回滚'
    case 'failed':
      return '失败'
    default:
      return '空闲'
  }
})

const phaseType = computed(() => {
  switch (transaction.value.phase) {
    case 'pending':
      return 'warning' as const
    case 'confirmed':
      return 'success' as const
    case 'failed':
      return 'error' as const
    default:
      return 'default' as const
  }
})

const verification = computed(() => preview.value?.verification ?? null)
</script>

<template>
  <div class="page-stack page-narrow">
    <!-- 待确认横幅：最高优先级，置顶 -->
    <TransactionBanner
      v-if="transaction.phase === 'pending'"
      :state="transaction"
      :confirming="confirming"
      :rolling-back="rollingBack"
      @confirm="doConfirm"
      @rollback="doRollback('用户从倒计时横幅回滚')"
      @expired="onExpired"
    />

    <Callout v-if="rulesRes.error.value" tone="danger" badge="错误" title="无法读取规则">
      {{ rulesRes.error.value }}
    </Callout>

    <!-- 当前系统状态（只读） -->
    <PanelCard title="系统状态" :subtitle="`采集于 ${fwStatus.data.value?.collectedAt ?? '—'}`">
      <div class="flex flex-wrap items-center gap-2">
        <NTag :type="phaseType" size="small" :bordered="false">事务：{{ phaseText }}</NTag>
        <NTag
          v-if="fwStatus.data.value"
          size="small"
          :bordered="false"
          :type="fwStatus.data.value.pf.readAvailable ? 'success' : 'default'"
        >
          PF：{{
            !fwStatus.data.value.pf.readAvailable
              ? '不可读'
              : fwStatus.data.value.pf.enabled == null
                ? '未知'
                : fwStatus.data.value.pf.enabled
                  ? '已启用'
                  : '已关闭'
          }}
        </NTag>
        <NTag
          v-if="fwStatus.data.value"
          size="small"
          :bordered="false"
          :type="fwStatus.data.value.anchor.present ? 'info' : 'default'"
        >
          Anchor：{{ fwStatus.data.value.anchor.present ? '已创建' : '未创建' }}
        </NTag>
        <NButton size="tiny" quaternary @click="refreshStatus">刷新</NButton>
      </div>
      <p
        v-if="fwStatus.data.value && !fwStatus.data.value.pf.readAvailable"
        class="mt-2 mb-0 text-[11px]"
        style="color: var(--color-text-tertiary)"
      >
        无法读取 PF 运行状态（需 root）。这不等于 PF 已关闭。
      </p>
    </PanelCard>

    <!-- 规则表 -->
    <PanelCard
      title="IPv6 入站规则"
      :subtitle="`共 ${localRules.length} 条，按顺序匹配，首个命中即生效`"
      :padded="false"
    >
      <template #actions>
        <NSpace :size="6" align="center">
          <NSelect
            v-model:value="templateSelect"
            size="tiny"
            placeholder="套用模板"
            :options="templates.map((t) => ({ label: t.displayName, value: t.key }))"
            style="width: 140px"
            @update:value="applyTemplate"
          />
          <NButton
            size="tiny"
            @click="
              () => {
                editingRule = null
                editorShow = true
              }
            "
          >
            添加规则
          </NButton>
        </NSpace>
      </template>

      <NDataTable
        :columns="columns"
        :data="localRules"
        :row-key="(row: FirewallRule) => `${row.id}-${row.position}`"
        :bordered="false"
        :single-line="false"
        size="small"
        :pagination="false"
        :loading="rulesRes.loading.value"
        :scroll-x="900"
      >
        <template #empty>
          <NEmpty description="还没有规则。可以从模板开始，或手动添加。" class="py-6" />
        </template>
      </NDataTable>
    </PanelCard>

    <!-- 默认拒绝 -->
    <PanelCard
      title="默认拒绝入站"
      subtitle="仅在选定的物理接口上拒绝未被上面规则放行的 IPv6 入站流量"
    >
      <Callout tone="warn" badge="高风险" title="配置前请确认">
        开启默认拒绝后，只有上面明确放行的端口可以访问。回环（lo0）与必需 ICMPv6
        由系统自动放行，不受此处影响。首次配置建议保留一个可用的本地终端会话。
      </Callout>

      <div class="mt-3">
        <div class="mb-1.5 text-[12px]" style="color: var(--color-text-secondary)">
          需要实施默认拒绝的接口（留空 = 不启用默认拒绝）
        </div>
        <NCheckboxGroup v-model:value="denyInterfaces" @update:value="dirty = true; refreshPreview()">
          <NSpace>
            <NCheckbox
              v-for="iface in ['en0', 'en1', 'en5', 'en6', 'en7']"
              :key="iface"
              :value="iface"
              :label="iface"
            />
          </NSpace>
        </NCheckboxGroup>
        <p class="mt-2 mb-0 text-[11px]" style="color: var(--color-text-tertiary)">
          只列出常见物理接口。lo0 与 utun* 不允许选入——拒绝它们会切断本机回环或远程管理通道。
        </p>
      </div>
    </PanelCard>

    <!-- 预览 -->
    <PanelCard title="规则预览" subtitle="实际会写入 PF anchor 的内容，包含系统强制添加的规则">
      <template #actions>
        <NSpace :size="6">
          <NButton size="tiny" quaternary :loading="previewLoading" @click="refreshPreview">
            重新生成
          </NButton>
        </NSpace>
      </template>

      <NSpin :show="previewLoading">
        <div v-if="preview">
          <!-- 校验结果 -->
          <NAlert
            v-if="verification && !verification.ok"
            type="error"
            :bordered="true"
            class="mb-3"
          >
            <template #header>校验未通过，无法应用</template>
            <ul class="m-0 list-disc pl-4 text-[12px]">
              <li v-for="(e, i) in verification.syntaxErrors ?? []" :key="`s${i}`">
                语法：{{ e.message }}
              </li>
              <li v-for="(v, i) in verification.invariantViolations ?? []" :key="`i${i}`">
                不变量 {{ v.invariant }}：{{ v.message }}
              </li>
            </ul>
          </NAlert>

          <div v-else class="mb-3 flex flex-wrap items-center gap-2 text-[12px]">
            <NTag type="success" size="small" :bordered="false">校验通过</NTag>
            <span style="color: var(--color-text-secondary)">
              {{ preview.ruleCount }} 条用户规则 ·
              {{ preview.enforced?.length ?? 0 }} 条强制规则 ·
              {{ verification?.stats.passRules ?? 0 }} pass /
              {{ verification?.stats.blockRules ?? 0 }} block
            </span>
          </div>

          <!-- 强制规则说明 -->
          <details v-if="preview.enforced && preview.enforced.length > 0" class="mb-3">
            <summary class="cursor-pointer text-[12px] font-medium">
              系统强制添加了 {{ preview.enforced.length }} 条规则（不可移除，点击查看原因）
            </summary>
            <ul class="mt-2 mb-0 list-none space-y-1.5 p-0 text-[11.5px]">
              <li
                v-for="e in preview.enforced"
                :key="e.key + e.rule"
                class="rounded px-2 py-1.5"
                style="background: var(--color-accent-soft)"
              >
                <code class="mono block break-all">{{ e.rule }}</code>
                <span style="color: var(--color-text-secondary)">{{ e.reason }}</span>
              </li>
            </ul>
          </details>

          <!-- 跳过的规则 -->
          <NAlert
            v-if="preview.skipped && preview.skipped.length > 0"
            type="warning"
            :bordered="false"
            class="mb-3"
          >
            <template #header>有 {{ preview.skipped.length }} 条规则未生效</template>
            <ul class="m-0 list-disc pl-4 text-[12px]">
              <li v-for="s in preview.skipped" :key="s.ruleId">
                {{ s.name }}：{{ s.reason }}
              </li>
            </ul>
          </NAlert>

          <pre
            class="mono m-0 max-h-80 overflow-auto rounded-lg p-3 text-[11px] leading-relaxed"
            style="background: var(--color-surface-sunken); border: 1px solid var(--color-border-subtle)"
            >{{ preview.content }}</pre
          >
        </div>
        <NEmpty v-else description="尚无可预览的规则" class="py-6" />
      </NSpin>
    </PanelCard>

    <!-- 操作区 -->
    <PanelCard title="操作">
      <div class="flex flex-wrap items-center gap-2">
        <!-- 保存始终可用：即使 dirty 因竞态显示为 false，用户也应能强制保存。
             重复保存是幂等的（整体覆盖），不会产生副作用。 -->
        <NButton size="small" :loading="saving" @click="save">
          保存规则{{ dirty ? '（有未保存改动）' : '' }}
        </NButton>

        <NTooltip>
          <template #trigger>
            <NButton
              type="primary"
              size="small"
              :loading="applying"
              :disabled="!verification?.ok || transaction.phase === 'pending'"
              @click="confirmApply"
            >
              应用规则
            </NButton>
          </template>
          <span v-if="transaction.phase === 'pending'">请先确认或回滚当前待确认的配置</span>
          <span v-else-if="!verification?.ok">规则未通过校验，无法应用</span>
          <span v-else>应用后需在 60 秒内确认，否则自动回滚</span>
        </NTooltip>

        <NButton
          v-if="transaction.phase !== 'idle'"
          size="small"
          :loading="rollingBack"
          @click="doRollback('用户主动回滚')"
        >
          回滚
        </NButton>

        <span v-if="dirty" class="text-[11px]" style="color: var(--color-warn)">
          改动了规则但尚未保存到数据库
        </span>
      </div>

      <p class="mt-3 mb-0 text-[11px] leading-relaxed" style="color: var(--color-text-tertiary)">
        保存只写数据库；应用才会改变系统防火墙。应用前会先用
        <code class="mono">pfctl -n -f</code> 做语法检查（只解析不加载），
        通过后才真正加载到本应用的 anchor。
      </p>
    </PanelCard>

    <RuleEditor
      v-model:show="editorShow"
      :rule="editingRule"
      :known-interfaces="knownInterfaces"
      @save="onRuleSaved"
    />
  </div>
</template>
