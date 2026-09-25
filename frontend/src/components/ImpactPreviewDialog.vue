<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { AlertTriangle, ArrowRight, FlaskConical, RefreshCw } from 'lucide-vue-next'
import { profileApi } from '@/api/domain'
import EvidencePathPanel from '@/components/common/EvidencePathPanel.vue'
import RiskBadge from '@/components/common/RiskBadge.vue'
import type { AllergenProfile } from '@/types/domain'
import type { ImpactCellChange, ImpactRouteResult, ProfileImpactPreview } from '@/types/impact'
import { impactChangeLabels, impactReasonLabels } from '@/types/impact'
import { percent } from '@/utils/format'

const props = defineProps<{ profile: AllergenProfile; proposedAllergens: string[] }>()
const open = defineModel<boolean>({ required: true })

const loading = ref(false)
const preview = ref<ProfileImpactPreview>()
const conflictMessage = ref('')
const errorMessage = ref('')
const selectedRoute = ref<ImpactRouteResult>()
const selectedChange = ref<ImpactCellChange>()

const selectedEvidence = computed(() => {
  const change = selectedChange.value
  if (!change) return undefined
  return change.change_type === 'removed' ? change.before_evidence : change.after_evidence
})
const biggestEvidence = computed(() => {
  const change = preview.value?.global_biggest_change?.change
  if (!change) return undefined
  return change.change_type === 'removed' ? change.before_evidence : change.after_evidence
})
const routeTotals = computed(() => {
  const totals = { added: 0, removed: 0, upgraded: 0, downgraded: 0 }
  for (const route of preview.value?.routes || []) {
    totals.added += route.added_count
    totals.removed += route.removed_count
    totals.upgraded += route.upgraded_count
    totals.downgraded += route.downgraded_count
  }
  return totals
})

watch(open, (visible) => { if (visible) void runPreview() })

async function runPreview() {
  loading.value = true
  preview.value = undefined
  conflictMessage.value = ''
  errorMessage.value = ''
  selectedRoute.value = undefined
  selectedChange.value = undefined
  try {
    const detail = await profileApi.detail(props.profile.id)
    const versions: Record<string, number> = {}
    for (const usage of detail.used_by_routes) {
      if (usage.route_status === 'active') versions[usage.route_code] = usage.route_version
    }
    if (!Object.keys(versions).length) versions['__none__'] = 1
    preview.value = await profileApi.impactPreview(props.profile.id, {
      allergens: props.proposedAllergens,
      expected_profile_version: props.profile.version,
      expected_route_versions: versions,
    })
    if (preview.value.routes.length) selectRoute(preview.value.routes[0])
  } catch (error) {
    const body = (error as { response?: { status?: number; data?: { error?: { code?: string; message?: string } } } }).response
    if (body?.status === 409 && body.data?.error?.code === 'profile_version_conflict') {
      conflictMessage.value = body.data.error.message || '过敏原谱在推演发起后已变化，请重新读取谱与引用路线'
    } else {
      errorMessage.value = body?.data?.error?.message || '推演未完成，请检查谱与路线输入'
    }
  } finally {
    loading.value = false
  }
}

function selectRoute(route: ImpactRouteResult) {
  selectedRoute.value = route
  selectedChange.value = route.changes[0]
}
function selectChange(change: ImpactCellChange) { selectedChange.value = change }
const changeLabel = (change: ImpactCellChange) => impactChangeLabels[change.change_type]
function cellTransition(change: ImpactCellChange) {
  const before = change.before ? `${change.before.risk_level} ${percent(change.before.max_raw_score)}` : '—'
  const after = change.after ? `${change.after.risk_level} ${percent(change.after.max_raw_score)}` : '—'
  return `${before} → ${after}`
}
const changeKey = (change: ImpactCellChange) => `${change.target_step_code}:${change.allergen}:${change.change_type}`
</script>

<template>
  <el-dialog v-model="open" :title="`影响推演 · ${profile.profile_code}`" width="min(1020px, 94vw)" destroy-on-close>
    <div class="preview-banner"><FlaskConical :size="16" /><span>只读推演：按谱 v{{ profile.version }} 与发起时的路线版本计算，不保存谱、不改动既有评估</span></div>
    <div class="allergen-compare">
      <div><span class="compare-label">当前谱</span><div class="allergen-tags"><span v-for="item in preview?.current_allergens || profile.allergens_json" :key="item" class="allergen-tag">{{ item }}</span></div></div>
      <ArrowRight :size="15" class="compare-arrow" />
      <div><span class="compare-label">拟修改</span><div class="allergen-tags"><span v-for="item in proposedAllergens" :key="item" class="allergen-tag proposed">{{ item }}</span></div></div>
    </div>

    <div v-if="loading" class="loading-state">正在按当前谱与拟修改谱分别计算引用路线…</div>

    <div v-else-if="conflictMessage" class="conflict-band">
      <AlertTriangle :size="18" />
      <div><strong>{{ conflictMessage }}</strong><span>关闭推演并刷新列表后，基于最新谱版本重新发起。</span></div>
    </div>
    <div v-else-if="errorMessage" class="conflict-band"><AlertTriangle :size="18" /><div><strong>{{ errorMessage }}</strong></div></div>

    <template v-else-if="preview">
      <div class="metric-strip">
        <div class="metric"><span>受影响生效路线</span><strong>{{ preview.effective_route_count }}</strong></div>
        <div class="metric"><span>已计算 / 无法计算</span><strong>{{ preview.calculated_route_count }} / {{ preview.uncalculable_routes.length }}</strong></div>
        <div class="metric"><span>新增 / 消失</span><strong>+{{ routeTotals.added }} / −{{ routeTotals.removed }}</strong></div>
        <div class="metric"><span>升级 / 降级</span><strong>↑{{ routeTotals.upgraded }} / ↓{{ routeTotals.downgraded }}</strong></div>
      </div>

      <div v-if="preview.global_biggest_change" class="biggest-band">
        <div class="biggest-copy">
          <span class="eyebrow">变化最大的证据路径</span>
          <strong>{{ preview.global_biggest_change.route_code }} · {{ preview.global_biggest_change.change.target_step_code }} · {{ preview.global_biggest_change.change.allergen }}</strong>
          <span class="biggest-meta">{{ impactChangeLabels[preview.global_biggest_change.change.change_type] }} · {{ cellTransition(preview.global_biggest_change.change) }}<template v-if="biggestEvidence"> · {{ biggestEvidence.path.join(' → ') }}</template></span>
        </div>
        <RiskBadge v-if="biggestEvidence" :level="biggestEvidence.risk_level" :score="biggestEvidence.raw_score" />
      </div>

      <div v-if="preview.uncalculable_routes.length" class="uncalculable-band">
        <AlertTriangle :size="16" />
        <div>
          <strong>{{ preview.uncalculable_routes.length }} 条生效路线无法计算</strong>
          <span v-for="entry in preview.uncalculable_routes" :key="entry.route_id" class="uncalculable-row">
            <code>{{ entry.route_code }}</code> v{{ entry.current_version }}（发起时 v{{ entry.expected_version || '—' }}）· {{ impactReasonLabels[entry.reason_code] || entry.reason_code }}：{{ entry.reason_message }}
          </span>
        </div>
      </div>

      <div v-if="!preview.routes.length" class="empty-state"><div><strong>没有可计算的受影响路线</strong><span>{{ preview.effective_route_count ? '全部路线均无法计算，见上方原因' : '当前没有生效路线引用该谱' }}</span></div></div>

      <div v-else class="section-grid preview-grid">
        <div class="stack">
          <div class="data-surface">
            <div class="surface-header"><h2>生效路线</h2><span>阈值 {{ preview.threshold_version }} · 最大深度 {{ preview.max_depth }}</span></div>
            <div class="route-list">
              <button v-for="route in preview.routes" :key="route.route_id" class="route-row" :class="{ active: selectedRoute?.route_id === route.route_id }" @click="selectRoute(route)">
                <span class="route-head"><strong class="mono">{{ route.route_code }}</strong><span class="muted">v{{ route.route_version }}</span></span>
                <span class="route-name">{{ route.product_name }}</span>
                <span class="route-counts">
                  <span v-if="route.added_count" class="count added">+{{ route.added_count }} 新增</span>
                  <span v-if="route.removed_count" class="count removed">−{{ route.removed_count }} 消失</span>
                  <span v-if="route.upgraded_count" class="count upgraded">↑{{ route.upgraded_count }} 升级</span>
                  <span v-if="route.downgraded_count" class="count downgraded">↓{{ route.downgraded_count }} 降级</span>
                  <span v-if="!route.changes.length" class="count">无单元变化</span>
                </span>
              </button>
            </div>
          </div>

          <div v-if="selectedRoute" class="data-surface">
            <div class="surface-header"><h2>{{ selectedRoute.route_code }} 矩阵单元变化</h2><span>{{ selectedRoute.changes.length }} 个变化单元 · 边版本 {{ selectedRoute.contact_edge_versions.map((edge) => `#${edge.edge_id}v${edge.edge_version}`).join(' ') || '无接触边' }}</span></div>
            <div v-if="!selectedRoute.changes.length" class="empty-state"><div><strong>该路线矩阵无变化</strong><span>拟修改的过敏原集合不改变任何单元的风险等级</span></div></div>
            <el-table v-else :data="selectedRoute.changes" :row-key="changeKey" size="small" highlight-current-row @row-click="selectChange">
              <el-table-column label="变化" width="86"><template #default="{ row }"><span class="change-pill" :class="row.change_type">{{ changeLabel(row) }}</span></template></el-table-column>
              <el-table-column label="目标步骤" min-width="120"><template #default="{ row }"><strong class="mono">{{ row.target_step_code }}</strong><div class="muted cell-sub">{{ row.target_step_name }}</div></template></el-table-column>
              <el-table-column prop="allergen" label="过敏原" min-width="100" />
              <el-table-column label="等级变化" min-width="150"><template #default="{ row }"><span class="mono">{{ cellTransition(row) }}</span></template></el-table-column>
              <el-table-column label="声明" width="80"><template #default="{ row }"><span class="status-pill" :class="row.declared ? 'accepted' : 'pending_review'">{{ row.declared ? '已声明' : '传播' }}</span></template></el-table-column>
            </el-table>
          </div>
        </div>
        <EvidencePathPanel :item="selectedEvidence" />
      </div>
    </template>

    <template #footer>
      <span class="footer-note">推演不保存谱、不改动既有评估；确认修改请回到编辑表单保存</span>
      <el-button :icon="RefreshCw" :loading="loading" :disabled="Boolean(conflictMessage)" @click="runPreview">重新推演</el-button>
      <el-button type="primary" @click="open = false">关闭</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.preview-banner { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; padding: 9px 12px; color: #285c76; background: #e7f1f6; border: 1px solid #a5c6d7; font-size: 12px; font-weight: 700; }
.allergen-compare { display: grid; grid-template-columns: 1fr auto 1fr; align-items: center; gap: 12px; margin-bottom: 14px; padding: 10px 12px; background: var(--surface); border: 1px solid var(--line); }
.compare-label { display: block; margin-bottom: 6px; color: var(--muted); font-size: 10px; font-weight: 800; }
.compare-arrow { color: var(--muted); }
.allergen-tag.proposed { color: #1d5c49; background: #e7f3ee; border-color: #a3cdbd; }
.conflict-band { display: flex; gap: 10px; padding: 13px 14px; color: #873a34; background: #faecea; border: 1px solid #dfb4b0; }
.conflict-band strong, .conflict-band span { display: block; }
.conflict-band span { margin-top: 4px; color: #96544e; font-size: 12px; }
.biggest-band { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-top: 14px; padding: 12px 14px; background: var(--surface); border: 1px solid var(--line-strong); border-left: 3px solid var(--accent); }
.biggest-copy strong, .biggest-copy span { display: block; }
.eyebrow { color: var(--muted); font-size: 10px; font-weight: 800; }
.biggest-copy strong { margin-top: 3px; font-size: 14px; }
.biggest-meta { margin-top: 4px; color: var(--muted); font-size: 12px; }
.uncalculable-band { display: flex; gap: 10px; margin-top: 14px; padding: 12px 14px; color: #705212; background: #fff7e2; border: 1px solid #dfc986; }
.uncalculable-band strong { display: block; font-size: 12px; }
.uncalculable-row { display: block; margin-top: 6px; color: #7c673a; font-size: 12px; }
.uncalculable-row code { font-family: "SFMono-Regular", Consolas, monospace; }
.preview-grid { margin-top: 14px; }
.route-list { display: grid; }
.route-row { display: grid; gap: 3px; padding: 11px 14px; text-align: left; background: none; border: 0; border-bottom: 1px solid var(--line); cursor: pointer; }
.route-row:hover { background: #f2f6f3; }
.route-row.active { background: #eaf1ee; box-shadow: inset 3px 0 var(--accent); }
.route-head { display: flex; align-items: center; gap: 8px; }
.route-head strong { font-size: 13px; }
.route-name { color: var(--muted); font-size: 12px; }
.route-counts { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 3px; }
.count { font-size: 11px; font-weight: 700; color: var(--muted); }
.count.added { color: #17604b; }
.count.removed { color: #913830; }
.count.upgraded { color: #a04f1d; }
.count.downgraded { color: #285c76; }
.change-pill { display: inline-flex; align-items: center; min-height: 22px; padding: 1px 7px; border: 1px solid; border-radius: 3px; font-size: 11px; font-weight: 800; }
.change-pill.added { color: #17604b; background: #e7f3ee; border-color: #a3cdbd; }
.change-pill.removed { color: #913830; background: #faeae8; border-color: #ddb2ad; }
.change-pill.upgraded { color: #a04f1d; background: #fff0e6; border-color: #e1b89d; }
.change-pill.downgraded { color: #285c76; background: #e7f1f6; border-color: #a5c6d7; }
.cell-sub { margin-top: 3px; font-size: 11px; }
.footer-note { float: left; margin-top: 8px; color: var(--muted); font-size: 12px; }
@media (max-width: 840px) { .allergen-compare { grid-template-columns: 1fr; } .compare-arrow { transform: rotate(90deg); justify-self: center; } .footer-note { display: block; float: none; margin-bottom: 10px; text-align: left; } }
</style>
