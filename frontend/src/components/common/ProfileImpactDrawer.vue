<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { AlertTriangle, ArrowDown, ArrowUp, PlayCircle, RefreshCw, ShieldAlert, XCircle } from 'lucide-vue-next'
import { profileApi } from '@/api/domain'
import RiskBadge from '@/components/common/RiskBadge.vue'
import EvidencePathPanel from '@/components/common/EvidencePathPanel.vue'
import type { AllergenProfile } from '@/types/domain'
import type { RiskItem } from '@/types/assessment'
import type { CellChange, CellChangeKind, ProfileImpactPreview, RouteVersionConflict } from '@/types/impact'
import { percent } from '@/utils/format'

const props = defineProps<{ modelValue: boolean; profile: AllergenProfile | null; initialAllergens?: string[] | null }>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()

interface RoutePin { route_id: number; route_code: string; product_name: string; route_status: string; route_version: number }

const open = computed({
  get: () => props.modelValue,
  set: (value) => emit('update:modelValue', value),
})

const loading = ref(false)
const running = ref(false)
const allergenInput = ref('')
const activePins = ref<RoutePin[]>([])
const readVersion = ref(0)
const result = ref<ProfileImpactPreview>()
const error = ref('')
const versionConflicts = ref<RouteVersionConflict[]>([])
const selectedEvidence = ref<RiskItem>()

const kindLabels: Record<CellChangeKind, string> = { added: '新增', removed: '消失', upgrade: '升级', downgrade: '降级' }
const proposedAllergens = computed(() => allergenInput.value.split(/[,，]/).map((item) => item.trim()).filter(Boolean))
const versionStale = computed(() => Boolean(props.profile && readVersion.value && props.profile.version !== readVersion.value))

async function reread() {
  if (!props.profile) return
  loading.value = true
  error.value = ''
  versionConflicts.value = []
  result.value = undefined
  selectedEvidence.value = undefined
  try {
    const detail = await profileApi.detail(props.profile.id)
    readVersion.value = detail.profile.version
    const draft = props.initialAllergens && props.initialAllergens.length ? props.initialAllergens : detail.profile.allergens_json
    allergenInput.value = draft.join(', ')
    activePins.value = detail.used_by_routes.filter((item) => item.route_status === 'active')
  } catch (err: any) {
    error.value = err?.response?.data?.error?.message || '重新读取失败，请稍后重试'
  } finally {
    loading.value = false
  }
}

watch(open, (value) => {
  if (value && props.profile) void reread()
  if (!value) {
    result.value = undefined
    error.value = ''
    versionConflicts.value = []
  }
})

async function runPreview() {
  if (!props.profile) return
  if (!proposedAllergens.value.length) {
    error.value = '拟修改的过敏原集合至少需要一个条目'
    return
  }
  running.value = true
  error.value = ''
  versionConflicts.value = []
  result.value = undefined
  selectedEvidence.value = undefined
  try {
    result.value = await profileApi.impactPreview(props.profile.id, {
      allergens: proposedAllergens.value,
      expected_version: readVersion.value,
      route_versions: activePins.value.map((item) => ({ route_id: item.route_id, expected_version: item.route_version })),
    })
  } catch (err: any) {
    const body = err?.response?.data?.error
    error.value = body?.message || '推演未完成，请检查输入或稍后重试'
    if (body?.code === 'route_version_conflict' && Array.isArray(body.details)) versionConflicts.value = body.details
    if (body?.code === 'profile_version_conflict') readVersion.value = 0
  } finally {
    running.value = false
  }
}

function evidenceFor(change: CellChange): RiskItem | undefined {
  return change.after_evidence || change.before_evidence
}
function selectChange(change: CellChange) { selectedEvidence.value = evidenceFor(change) }
function kindClass(kind: CellChangeKind) { return `change-kind ${kind}` }
</script>

<template>
  <el-drawer v-model="open" title="过敏原谱影响推演（只读）" size="76%" destroy-on-close>
    <div v-loading="loading" class="impact-drawer">
      <div class="impact-notice"><ShieldAlert :size="17" /><div><strong>推演不会保存谱，也不会改动既有评估。</strong><span>结果按发起时读取的谱版本与生效路线版本计算；确认结果后请关闭本抽屉，再通过“编辑过敏原谱”显式保存。</span></div></div>

      <div v-if="profile" class="impact-head">
        <div><strong class="mono">{{ profile.profile_code }}</strong><span>{{ profile.material_name }}</span><code>读取版本 v{{ readVersion || '—' }}</code></div>
        <el-button :icon="RefreshCw" :loading="loading" @click="reread">重新读取谱与路线版本</el-button>
      </div>
      <el-alert v-if="versionStale" type="warning" :closable="false" show-icon title="页面中的谱版本已不是推演读取的版本，请先重新读取。" class="impact-alert" />

      <div class="impact-form">
        <label>
          <span>拟修改的过敏原集合（逗号分隔，按保存口径去重）</span>
          <el-input v-model="allergenInput" type="textarea" :rows="2" placeholder="例如：Milk, Egg" />
        </label>
        <div class="impact-run">
          <el-button type="primary" :icon="PlayCircle" :loading="running" :disabled="versionStale || !readVersion" @click="runPreview">运行只读推演</el-button>
          <p>生效引用路线 <strong>{{ activePins.length }}</strong> 条<span v-if="activePins.length">：{{ activePins.map((item) => `${item.route_code}@v${item.route_version}`).join('、') }}</span></p>
        </div>
      </div>

      <el-alert v-if="error" type="error" :closable="false" show-icon class="impact-alert">
        <template #title>{{ error }}</template>
        <template v-if="versionConflicts.length" #default>
          <ul class="conflict-list">
            <li v-for="conflict in versionConflicts" :key="conflict.route_id"><strong class="mono">{{ conflict.route_code }}</strong><span v-if="conflict.reason === 'route_version_missing'">缺少该路线的发起版本（当前 v{{ conflict.current_version }}）</span><span v-else>发起版本 v{{ conflict.pinned_version }} → 当前 v{{ conflict.current_version }}</span></li>
          </ul>
          <el-button size="small" :icon="RefreshCw" @click="reread">重新读取后再推演</el-button>
        </template>
      </el-alert>

      <template v-if="result">
        <div class="impact-summary">
          <div class="summary-count"><span>生效路线 / 已计算 / 无法计算</span><strong>{{ result.active_route_count }} / {{ result.computed_route_count }} / {{ result.failed_route_count }}</strong></div>
          <div class="summary-count" v-for="kind in (['added','removed','upgrade','downgrade'] as CellChangeKind[])" :key="kind" :class="kind">
            <span>{{ kindLabels[kind] }}单元</span><strong>{{ result.change_counts[kind] ?? 0 }}</strong>
          </div>
        </div>
        <div class="threshold-line">阈值 {{ result.threshold_version }} · 最大深度 {{ result.max_depth }} · 谱 v{{ result.profile_version }}</div>

        <div v-if="result.biggest_change" class="biggest-band">
          <AlertTriangle :size="18" />
          <div>
            <strong>变化最大的证据路径：{{ result.biggest_change_route_code }} · {{ result.biggest_change.target_step_code }} × {{ result.biggest_change.allergen }}（{{ kindLabels[result.biggest_change.kind] }}）</strong>
            <span class="mono">{{ result.biggest_change.after_evidence?.path.join(' → ') || result.biggest_change.before_evidence?.path.join(' → ') }}</span>
            <span>分数变化 {{ percent(result.biggest_change.score_delta) }}<template v-if="result.biggest_change.risk_level_changed"> · 风险等级 {{ result.biggest_change.before?.risk_level }} → {{ result.biggest_change.after?.risk_level }}</template></span>
            <el-button size="small" text type="primary" @click="selectChange(result.biggest_change!)">查看该路径证据</el-button>
          </div>
        </div>

        <div class="impact-grid">
          <div class="stack">
            <div v-for="route in result.routes" :key="route.route_id" class="data-surface route-card">
              <div class="surface-header">
                <h2>{{ route.route_code }} · {{ route.product_name }} <code>v{{ route.route_version }}</code></h2>
                <span v-if="route.referencing_steps.length">引用步骤 {{ route.referencing_steps.join(', ') }}</span>
              </div>
              <div v-if="!route.computed" class="route-error">
                <XCircle :size="17" />
                <div><strong>无法计算</strong><span>{{ route.error_message }}（{{ route.error_code }}）</span></div>
              </div>
              <template v-else>
                <div class="route-counts">
                  <span v-for="kind in (['added','removed','upgrade','downgrade'] as CellChangeKind[])" :key="kind" :class="kindClass(kind)">{{ kindLabels[kind] }} {{ route.change_counts[kind] ?? 0 }}</span>
                  <span class="highest">最高风险 <RiskBadge :level="route.highest_risk_before" /> → <RiskBadge :level="route.highest_risk_after" /></span>
                </div>
                <el-table v-if="route.changes.length" :data="route.changes" size="small" highlight-current-row @row-click="selectChange">
                  <el-table-column label="变化" width="78"><template #default="{ row }"><span :class="kindClass(row.kind)">{{ kindLabels[row.kind as CellChangeKind] }}</span></template></el-table-column>
                  <el-table-column label="目标步骤" min-width="120"><template #default="{ row }"><strong class="mono">{{ row.target_step_code }}</strong><div class="muted cell-sub">{{ row.target_step_name }}</div></template></el-table-column>
                  <el-table-column prop="allergen" label="过敏原" min-width="95" />
                  <el-table-column label="分数 前 → 后" min-width="160">
                    <template #default="{ row }"><span class="mono score-line">{{ row.before ? percent(row.before.max_raw_score) : '—' }} → {{ row.after ? percent(row.after.max_raw_score) : '—' }}</span><component :is="row.score_delta >= 0 ? ArrowUp : ArrowDown" :size="13" :class="row.score_delta >= 0 ? 'delta-up' : 'delta-down'" /></template>
                  </el-table-column>
                  <el-table-column label="风险 前 → 后" min-width="150"><template #default="{ row }"><RiskBadge v-if="row.before" :level="row.before.risk_level" /><span v-else class="muted">—</span><span class="muted"> → </span><RiskBadge v-if="row.after" :level="row.after.risk_level" /><span v-else class="muted">—</span></template></el-table-column>
                  <template #empty><div class="empty-state compact"><strong>该路线矩阵单元无变化</strong></div></template>
                </el-table>
                <div v-else class="empty-state compact"><strong>该路线矩阵单元无变化</strong><span>拟修改集合对本路线传播结果没有影响</span></div>
              </template>
            </div>
            <div v-if="!result.routes.length" class="empty-state"><div><strong>没有生效路线引用该谱</strong><span>草稿与停用路线不参与推演</span></div></div>
          </div>
          <EvidencePathPanel :item="selectedEvidence" />
        </div>
      </template>
    </div>
  </el-drawer>
</template>

<style scoped>
.impact-drawer { display: grid; gap: 14px; padding-right: 4px; }
.impact-notice { display: flex; gap: 10px; padding: 11px 13px; color: #1d4f3f; background: #eaf3ee; border: 1px solid #b5d2c5; }
.impact-notice strong, .impact-notice span { display: block; }.impact-notice strong { font-size: 12.5px; }.impact-notice span { margin-top: 3px; color: #41594f; font-size: 12px; }
.impact-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.impact-head > div { display: flex; align-items: baseline; gap: 10px; flex-wrap: wrap; }.impact-head span { color: var(--muted); font-size: 12px; }.impact-head code { color: var(--muted); }
.impact-alert { margin: 0; }
.impact-form { display: grid; grid-template-columns: minmax(0, 1fr) 300px; gap: 14px; align-items: start; }
.impact-form label span { display: block; margin-bottom: 6px; color: var(--muted); font-size: 12px; font-weight: 700; }
.impact-run p { margin: 9px 0 0; color: var(--muted); font-size: 12px; line-height: 1.5; }.impact-run strong { color: var(--text); }
.impact-summary { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); border: 1px solid var(--line); background: var(--surface); }
.summary-count { padding: 11px 13px; border-right: 1px solid var(--line); }.summary-count:last-child { border-right: 0; }.summary-count span { display: block; color: var(--muted); font-size: 11px; }.summary-count strong { display: block; margin-top: 4px; font-size: 19px; font-variant-numeric: tabular-nums; }
.summary-count.added strong { color: #1d6f54; }.summary-count.removed strong { color: #6b716d; }.summary-count.upgrade strong { color: #a04f1d; }.summary-count.downgrade strong { color: #2d6380; }
.threshold-line { color: var(--muted); font-size: 12px; }
.biggest-band { display: flex; gap: 10px; padding: 12px 14px; color: #705212; background: #fff7e2; border: 1px solid #dfc986; }
.biggest-band strong, .biggest-band span { display: block; }.biggest-band strong { font-size: 13px; }.biggest-band span { margin-top: 4px; color: #7c673a; font-size: 12px; }.biggest-band .el-button { margin-top: 4px; padding: 0; }
.impact-grid { display: grid; grid-template-columns: minmax(0, 1fr) minmax(280px, 34%); gap: 14px; align-items: start; }
.route-card .surface-header code { margin-left: 6px; color: var(--muted); }
.route-error { display: flex; gap: 9px; align-items: center; padding: 13px 14px; color: #873a34; }.route-error strong, .route-error span { display: block; }.route-error span { margin-top: 2px; font-size: 12px; }
.route-counts { display: flex; flex-wrap: wrap; gap: 7px; padding: 10px 14px; border-bottom: 1px solid var(--line); }
.change-kind { display: inline-flex; align-items: center; min-height: 23px; padding: 1px 8px; border: 1px solid; border-radius: 3px; font-size: 12px; font-weight: 800; white-space: nowrap; }
.change-kind.added { color: #17604b; background: #e7f3ee; border-color: #a3cdbd; }.change-kind.removed { color: #5f6863; background: #efefed; border-color: #c6ccc8; }
.change-kind.upgrade { color: #a04f1d; background: #fff0e6; border-color: #e1b89d; }.change-kind.downgrade { color: #285c76; background: #e8f2f7; border-color: #a3c4d5; }
.highest { margin-left: auto; display: inline-flex; align-items: center; gap: 6px; color: var(--muted); font-size: 12px; }
.score-line { font-size: 12px; }.delta-up { color: #a04f1d; vertical-align: -2px; margin-left: 4px; }.delta-down { color: #2d6380; vertical-align: -2px; margin-left: 4px; }
.cell-sub { margin-top: 2px; font-size: 11px; }
.empty-state.compact { min-height: 90px; padding: 16px; }.empty-state.compact span { font-size: 12px; }
.conflict-list { margin: 6px 0 8px; padding-left: 18px; }.conflict-list li { margin-bottom: 3px; }.conflict-list span { margin-left: 6px; }
@media (max-width: 1100px) { .impact-grid { grid-template-columns: 1fr; }.impact-form { grid-template-columns: 1fr; }.impact-summary { grid-template-columns: repeat(2, 1fr); }.summary-count:nth-child(2n) { border-right: 0; }.summary-count { border-bottom: 1px solid var(--line); } }
</style>
