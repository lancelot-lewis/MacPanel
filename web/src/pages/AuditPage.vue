<script setup lang="ts">
import { computed, ref } from 'vue'
import { api } from '../api/client'
import { useResource } from '../lib/useResource'
import { formatTime } from '../lib/format'
import PanelCard from '../components/PanelCard.vue'
import Callout from '../components/Callout.vue'

const limit = ref(50)
const audit = useResource(() => api.audit(limit.value), { intervalMs: 15000 })

const items = computed(() => audit.data.value?.items ?? [])

const resultStyle: Record<string, { label: string; color: string }> = {
  success: { label: '成功', color: 'var(--color-ok)' },
  failure: { label: '失败', color: 'var(--color-danger)' },
  denied: { label: '拒绝', color: 'var(--color-warn)' },
}

function detailText(detail?: Record<string, unknown>): string {
  if (!detail) return ''
  const entries = Object.entries(detail)
  if (entries.length === 0) return ''
  return entries.map(([k, v]) => `${k}=${String(v)}`).join(' ')
}
</script>

<template>
  <div class="mx-auto flex max-w-5xl flex-col gap-4">
    <Callout v-if="audit.error.value" tone="danger" badge="错误" title="无法获取审计日志">
      {{ audit.error.value }}
    </Callout>

    <PanelCard title="操作审计" subtitle="记录高风险操作与接口调用结果，不包含密码、令牌等敏感内容">
      <template #actions>
        <select
          v-model.number="limit"
          class="rounded-md border border-[var(--color-border-subtle)] bg-[var(--color-surface)] px-2 py-1 text-[12px]"
          aria-label="显示条数"
          @change="audit.refresh()"
        >
          <option :value="50">最近 50 条</option>
          <option :value="100">最近 100 条</option>
          <option :value="200">最近 200 条</option>
        </select>
      </template>

      <div v-if="items.length === 0" class="text-[13px] text-[var(--color-text-secondary)]">
        <span v-if="audit.loading.value">正在读取…</span>
        <span v-else>暂无审计记录。访问其他页面后会自动产生读取记录。</span>
      </div>

      <div v-else class="overflow-x-auto">
        <table class="w-full border-collapse text-[12px]">
          <thead>
            <tr class="border-b border-[var(--color-border-subtle)] text-left">
              <th class="py-2 pr-3 font-medium text-[var(--color-text-secondary)]">时间</th>
              <th class="py-2 pr-3 font-medium text-[var(--color-text-secondary)]">动作</th>
              <th class="py-2 pr-3 font-medium text-[var(--color-text-secondary)]">调用方</th>
              <th class="py-2 pr-3 font-medium text-[var(--color-text-secondary)]">结果</th>
              <th class="py-2 font-medium text-[var(--color-text-secondary)]">补充</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="e in items"
              :key="e.id"
              class="border-b border-[var(--color-border-subtle)] last:border-0"
            >
              <td class="mono whitespace-nowrap py-1.5 pr-3 opacity-80">{{ formatTime(e.at) }}</td>
              <td class="mono py-1.5 pr-3">{{ e.action }}</td>
              <td class="mono py-1.5 pr-3 break-all opacity-80">{{ e.actor }}</td>
              <td class="whitespace-nowrap py-1.5 pr-3">
                <span
                  class="rounded px-1.5 py-0.5 text-[10px] font-medium"
                  :style="{
                    background: `color-mix(in oklab, ${resultStyle[e.result]?.color ?? 'var(--color-text-secondary)'} 14%, transparent)`,
                    color: resultStyle[e.result]?.color ?? 'var(--color-text-secondary)',
                  }"
                >
                  {{ resultStyle[e.result]?.label ?? e.result }}
                </span>
              </td>
              <td class="mono py-1.5 break-all text-[11px] opacity-70">
                {{ e.target }} {{ detailText(e.detail) }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </PanelCard>

    <Callout tone="info" badge="说明">
      审计日志保存在本机 SQLite 数据库中。M2 起，防火墙规则应用、回滚、定时重启等高风险操作
      会记录完整的前后状态与操作者身份。
    </Callout>
  </div>
</template>
