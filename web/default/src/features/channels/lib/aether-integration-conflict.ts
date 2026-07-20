import type { AetherIntegrationConflictPayload } from '../types'

export type AetherConfigSnapshot = {
  route_profile: string
  execution_mode: 'direct_channel' | 'disabled'
  enabled: boolean
  capability_version: string
}

export type AetherConflictDetails = {
  currentRevision: number
  currentConfig: Partial<AetherConfigSnapshot>
  fieldDiff: Array<{
    field: keyof AetherConfigSnapshot
    requested: string | boolean
    current: string | boolean
  }>
}

export const AETHER_CONFIG_FIELDS = [
  'route_profile',
  'execution_mode',
  'enabled',
  'capability_version',
] as const

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function assignAetherConfigField(
  config: Partial<AetherConfigSnapshot>,
  field: keyof AetherConfigSnapshot,
  value: unknown
): void {
  if (field === 'enabled') {
    if (typeof value === 'boolean') config[field] = value
    return
  }
  if (field === 'execution_mode') {
    if (value === 'direct_channel' || value === 'disabled') {
      config[field] = value
    }
    return
  }
  if (typeof value === 'string') config[field] = value
}

export function buildAetherConflictDetails(
  payload: AetherIntegrationConflictPayload,
  draft: AetherConfigSnapshot
): AetherConflictDetails | null {
  if (
    typeof payload.current_revision !== 'number' ||
    !Number.isInteger(payload.current_revision) ||
    payload.current_revision < 0
  ) {
    return null
  }

  let rawCurrent: Record<string, unknown> = {}
  if (isRecord(payload.current_config)) {
    rawCurrent = payload.current_config
  } else if (isRecord(payload.current)) {
    rawCurrent = payload.current
  }
  const currentConfig: Partial<AetherConfigSnapshot> = {}

  for (const field of AETHER_CONFIG_FIELDS) {
    assignAetherConfigField(currentConfig, field, rawCurrent[field])
  }

  const rawDiff = isRecord(payload.diff) ? payload.diff : {}
  for (const field of AETHER_CONFIG_FIELDS) {
    if (currentConfig[field] !== undefined) continue
    const fieldDiff = rawDiff[field]
    if (!isRecord(fieldDiff)) continue
    assignAetherConfigField(currentConfig, field, fieldDiff.current)
  }

  const fieldDiff: AetherConflictDetails['fieldDiff'] = []
  for (const field of AETHER_CONFIG_FIELDS) {
    const current = currentConfig[field]
    if (current !== undefined && current !== draft[field]) {
      fieldDiff.push({
        field,
        requested: draft[field],
        current,
      })
    }
  }

  return {
    currentRevision: payload.current_revision,
    currentConfig,
    fieldDiff,
  }
}
