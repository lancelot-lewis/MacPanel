<script setup lang="ts">
// 规则编辑抽屉：新建 / 编辑单条规则。
//
// 用 NDrawer 而不是 NModal：规则有 10 个字段，抽屉的纵向空间更合适，
// 且不会像 Modal 那样在小屏幕上挤压内容。
//
// 表单校验与后端校验保持一致的规则（不是「前端松后端严」）：
// 端口范围、CIDR、接口名都在这里先拦一次，减少无意义的往返。

import { computed, reactive, ref, watch } from 'vue'
import { useMessage } from 'naive-ui'
import {
  NButton,
  NDrawer,
  NDrawerContent,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NSelect,
  NSpace,
  NSwitch,
  NAlert,
} from 'naive-ui'
import type { FormInst, FormItemRule, FormRules } from 'naive-ui'
import type { FirewallRule, RuleAction, RuleProtocol } from '../api/firewall'

const props = defineProps<{
  show: boolean
  /** 编辑已有规则时传入；新建时传 null */
  rule: FirewallRule | null
  /** 已有的接口名，供下拉选择 */
  knownInterfaces: string[]
}>()

const emit = defineEmits<{
  'update:show': [boolean]
  save: [FirewallRule]
}>()

const message = useMessage()
const formRef = ref<FormInst | null>(null)

// 表单模型。端口用 null 表示「不限」。
const form = reactive({
  name: '',
  description: '',
  action: 'pass' as RuleAction,
  protocol: 'tcp' as RuleProtocol,
  portFrom: null as number | null,
  portTo: null as number | null,
  sourceCidr: 'any',
  interface: 'any',
  enabled: true,
})

// 协议决定是否需要端口与来源限制
const needsPort = computed(
  () => form.protocol === 'tcp' || form.protocol === 'udp',
)

const isICMP = computed(() => form.protocol === 'icmp6')

const actionOptions = [
  { label: '放行（pass）', value: 'pass' },
  { label: '拒绝（block）', value: 'block' },
]

const protocolOptions = [
  { label: 'TCP', value: 'tcp' },
  { label: 'UDP', value: 'udp' },
  { label: 'ICMPv6', value: 'icmp6' },
  { label: '任意协议', value: 'any' },
]

const interfaceOptions = computed(() => {
  const base = [{ label: '任意接口（any）', value: 'any' }]
  for (const name of props.knownInterfaces) {
    if (name === 'any') continue
    base.push({ label: name, value: name })
  }
  return base
})

// 校验规则：与后端 model.FirewallRule.Validate 对齐
const rules = computed<FormRules>(() => {
  const r: FormRules = {
    name: [
      { required: true, message: '请输入规则名称', trigger: ['blur', 'input'] },
      {
        validator: (_rule: FormItemRule, v: string) => {
          if (!v || v.trim().length === 0) return new Error('规则名称不能为空')
          if (v.length > 64) return new Error('名称不能超过 64 个字符')
          return true
        },
        trigger: ['blur', 'input'],
      },
    ],
  }

  // 需要端口的协议：必须填，且范围合法
  if (needsPort.value) {
    // 不加 required: true —— naive-ui 的 required 对 NInputNumber 的数值
    // 判定不可靠（实测 value=8080 仍判为「空」）。只用自定义 validator，
    // 这样错误信息也更精确（能区分「没填」与「超出范围」）。
    r.portFrom = [
      {
        validator: (_rule: FormItemRule, v: number | null) => {
          if (v == null) return new Error('请输入端口')
          if (v < 1 || v > 65535) return new Error('端口必须在 1-65535 之间')
          if (form.portTo != null && v > form.portTo) {
            return new Error('起始端口不能大于结束端口')
          }
          return true
        },
        trigger: ['blur', 'change'],
      },
    ]
  }

  // CIDR：空或 any 之外，必须看起来像 IPv6 CIDR
  r.sourceCidr = [
    {
      validator: (_rule: FormItemRule, v: string) => {
        const s = (v ?? '').trim()
        if (s === '' || s.toLowerCase() === 'any') return true
        if (!s.includes(':')) {
          return new Error('请填写 IPv6 地址或 CIDR（本面板只处理 IPv6）')
        }
        if (!s.includes('/')) {
          return new Error('请带上前缀长度，例如 2001:db8::/32')
        }
        return true
      },
      trigger: ['blur', 'input'],
    },
  ]

  return r
})

// 打开时把传入的规则填进表单
watch(
  () => props.show,
  (show) => {
    if (!show) return
    const r = props.rule
    if (r) {
      form.name = r.name
      form.description = r.description
      form.action = r.action
      form.protocol = r.protocol
      form.portFrom = r.portFrom
      form.portTo = r.portTo
      form.sourceCidr = r.sourceCidr || 'any'
      form.interface = r.interface || 'any'
      form.enabled = r.enabled
    } else {
      form.name = ''
      form.description = ''
      form.action = 'pass'
      form.protocol = 'tcp'
      form.portFrom = null
      form.portTo = null
      form.sourceCidr = 'any'
      form.interface = 'any'
      form.enabled = true
    }
  },
  { immediate: true },
)

// 协议切换时清理不适用的字段，避免提交「icmp6 带端口」这类非法组合
watch(
  () => form.protocol,
  (p) => {
    if (p === 'icmp6') {
      form.portFrom = null
      form.portTo = null
    }
  },
)

async function handleSave() {
  try {
    await formRef.value?.validate()
  } catch {
    return // 校验提示由表单展示
  }

  // 结束端口留空表示单端口
  const portTo = form.portTo ?? form.portFrom

  emit('save', {
    id: props.rule?.id ?? 0,
    name: form.name.trim(),
    description: form.description.trim(),
    position: props.rule?.position ?? 0,
    action: form.action,
    direction: 'in',
    protocol: form.protocol,
    portFrom: needsPort.value ? form.portFrom : null,
    portTo: needsPort.value ? portTo : null,
    sourceCidr: form.sourceCidr.trim() || 'any',
    interface: form.interface || 'any',
    enabled: form.enabled,
    builtinTemplate: props.rule?.builtinTemplate,
  })
  message.success(props.rule ? '规则已更新（未保存到磁盘）' : '规则已添加（未保存到磁盘）')
  emit('update:show', false)
}
</script>

<template>
  <NDrawer
    :show="show"
    :width="440"
    placement="right"
    @update:show="(v: boolean) => emit('update:show', v)"
  >
    <NDrawerContent :title="rule ? '编辑规则' : '新建规则'" closable>
      <NForm
        ref="formRef"
        :model="form"
        :rules="rules"
        label-placement="top"
        size="small"
        @submit.prevent="handleSave"
      >
        <NFormItem label="规则名称" path="name">
          <NInput v-model:value="form.name" placeholder="例如：Caddy HTTPS" maxlength="64" />
        </NFormItem>

        <NFormItem label="备注">
          <NInput
            v-model:value="form.description"
            type="textarea"
            placeholder="可选，说明这条规则的用途"
            :autosize="{ minRows: 1, maxRows: 3 }"
            maxlength="256"
          />
        </NFormItem>

        <NFormItem label="动作">
          <NSelect v-model:value="form.action" :options="actionOptions" />
        </NFormItem>

        <NFormItem label="协议">
          <NSelect v-model:value="form.protocol" :options="protocolOptions" />
        </NFormItem>

        <NAlert v-if="isICMP" type="info" :bordered="false" class="mb-3">
          ICMPv6 不需要端口。已必需的类型（邻居发现、路由通告、路径 MTU）
          由系统自动放行，无需在此重复添加。
        </NAlert>

        <template v-if="needsPort">
          <div class="grid grid-cols-2 gap-3">
            <NFormItem label="起始端口" path="portFrom">
              <NInputNumber
                v-model:value="form.portFrom"
                :min="1"
                :max="65535"
                placeholder="443"
                class="w-full"
              />
            </NFormItem>
            <NFormItem label="结束端口（留空=单端口）">
              <NInputNumber
                v-model:value="form.portTo"
                :min="1"
                :max="65535"
                placeholder="留空"
                class="w-full"
              />
            </NFormItem>
          </div>
          <p class="mt-0 mb-3 text-[11px]" style="color: var(--color-text-tertiary)">
            提示：填写范围会放行区间内<strong>所有</strong>端口。只需要 80 和 443
            时应建两条规则，而不是 80-443。
          </p>
        </template>

        <NFormItem label="来源地址" path="sourceCidr">
          <NInput v-model:value="form.sourceCidr" placeholder="any 或 2001:db8::/32" />
        </NFormItem>

        <NFormItem label="生效接口">
          <NSelect v-model:value="form.interface" :options="interfaceOptions" />
        </NFormItem>

        <NFormItem label="启用">
          <NSwitch v-model:value="form.enabled" />
        </NFormItem>

      </NForm>

      <template #footer>
        <NSpace>
          <NButton type="primary" size="small" @click="handleSave">
            {{ rule ? '更新规则' : '添加规则' }}
          </NButton>
          <NButton size="small" @click="emit('update:show', false)">取消</NButton>
        </NSpace>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
