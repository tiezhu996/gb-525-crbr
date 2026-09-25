import type { RiskItem, MatrixCell } from './assessment'
import type { RiskLevel } from './risk'

export type CellChangeKind = 'added' | 'removed' | 'upgrade' | 'downgrade'

export interface CellChange {
  kind: CellChangeKind
  target_step_code: string
  target_step_name: string
  allergen: string
  declared: boolean
  before?: MatrixCell
  after?: MatrixCell
  score_delta: number
  risk_rank_delta: number
  risk_level_changed: boolean
  before_evidence?: RiskItem
  after_evidence?: RiskItem
}

export interface ImpactAllergenDelta {
  added: string[]
  removed: string[]
}

export interface RouteVersionConflict {
  route_id: number
  route_code: string
  pinned_version: number
  current_version: number
  reason: 'route_version_missing' | 'route_version_conflict'
}

export interface ImpactRoutePreview {
  route_id: number
  route_code: string
  product_name: string
  route_version: number
  referencing_steps: string[]
  computed: boolean
  error_code?: string
  error_message?: string
  allergen_delta: ImpactAllergenDelta
  changes: CellChange[]
  change_counts: Record<CellChangeKind, number>
  biggest_change?: CellChange
  highest_risk_before: RiskLevel
  highest_risk_after: RiskLevel
}

export interface ProfileImpactPreview {
  profile_id: number
  profile_code: string
  profile_version: number
  expected_profile_version: number
  allergen_delta: ImpactAllergenDelta
  active_route_count: number
  computed_route_count: number
  failed_route_count: number
  routes: ImpactRoutePreview[]
  change_counts: Record<CellChangeKind, number>
  biggest_change?: CellChange
  biggest_change_route_id?: number
  biggest_change_route_code?: string
  threshold_version: string
  max_depth: number
}

export interface ProfileImpactRequest {
  allergens: string[]
  expected_version: number
  route_versions: Array<{ route_id: number; expected_version: number }>
}
