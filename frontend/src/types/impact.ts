import type { RiskLevel } from './risk'
import type { EdgeEvidence } from './assessment'

export type ImpactChangeType = 'added' | 'removed' | 'upgraded' | 'downgraded'

export const impactChangeLabels: Record<ImpactChangeType, string> = {
  added: '新增',
  removed: '消失',
  upgraded: '升级',
  downgraded: '降级',
}

export const impactReasonLabels: Record<string, string> = {
  route_version_conflict: '路线版本已变化',
  route_version_unknown: '缺少路线版本',
  profile_missing: '引用谱不可用',
  profile_json_invalid: '谱内容无法解析',
  graph_invalid: '接触图结构无效',
  propagation_failed: '传播计算失败',
  computation_failed: '计算失败',
}

export interface ImpactMatrixCellView {
  target_step_code: string
  target_step_name: string
  allergen: string
  max_raw_score: number
  risk_level: RiskLevel
  path_count: number
  declared: boolean
}

export interface ImpactEvidenceView {
  allergen: string
  source_profile_id: number
  source_profile_code: string
  source_material: string
  source_step_code: string
  target_step_code: string
  target_step_name: string
  path: string[]
  raw_score: number
  risk_level: RiskLevel
  declared: boolean
  critical_edge?: EdgeEvidence
  cleaning_evidence: EdgeEvidence[]
  threshold_version: string
}

export interface ImpactCellChange {
  change_type: ImpactChangeType
  target_step_code: string
  target_step_name: string
  allergen: string
  before?: ImpactMatrixCellView
  after?: ImpactMatrixCellView
  level_delta: number
  score_delta: number
  declared: boolean
  before_evidence?: ImpactEvidenceView
  after_evidence?: ImpactEvidenceView
}

export interface ImpactEdgeVersion {
  edge_id: number
  edge_version: number
  enabled: boolean
}

export interface ImpactRouteResult {
  route_id: number
  route_code: string
  product_name: string
  route_version: number
  added_count: number
  removed_count: number
  upgraded_count: number
  downgraded_count: number
  changes: ImpactCellChange[]
  biggest_change?: ImpactCellChange
  contact_edge_versions: ImpactEdgeVersion[]
}

export interface ImpactUncalculableRoute {
  route_id: number
  route_code: string
  product_name: string
  current_version: number
  expected_version: number
  reason_code: string
  reason_message: string
}

export interface GlobalImpactChange {
  route_id: number
  route_code: string
  product_name: string
  route_version: number
  change: ImpactCellChange
}

export interface ProfileImpactPreview {
  profile_id: number
  profile_code: string
  profile_version: number
  current_allergens: string[]
  proposed_allergens: string[]
  added_allergens: string[]
  removed_allergens: string[]
  threshold_version: string
  max_depth: number
  effective_route_count: number
  calculated_route_count: number
  routes: ImpactRouteResult[]
  uncalculable_routes: ImpactUncalculableRoute[]
  global_biggest_change?: GlobalImpactChange
}

export interface ProfileImpactPreviewRequest {
  allergens: string[]
  expected_profile_version: number
  expected_route_versions: Record<string, number>
}
