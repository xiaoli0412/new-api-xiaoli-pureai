import type { AetherIntegration } from '../types'
import type { AetherConfigSnapshot } from './aether-integration-conflict'

export type AetherIntegrationForm = {
  instanceID: string
  routeProfile: string
  executionMode: 'direct_channel' | 'disabled'
  enabled: boolean
  capabilityVersion: string
  controlSecret: string
  relaySigningSecret: string
  baseRevision?: number
}

export type AetherHydrationIdentity = {
  channelId: number
  configRevision: number | null
}

export type AetherSyncStatus =
  | 'conflict'
  | 'out_of_sync'
  | 'synchronized'
  | 'not_synchronized'

export const AETHER_INITIAL_FORM: AetherIntegrationForm = {
  instanceID: '',
  routeProfile: '',
  executionMode: 'direct_channel',
  enabled: true,
  capabilityVersion: '',
  controlSecret: '',
  relaySigningSecret: '',
  baseRevision: undefined,
}

export function hasRequiredAetherConfiguration(
  form: Pick<AetherIntegrationForm, 'instanceID' | 'routeProfile'>
): boolean {
  return form.instanceID.trim() !== '' && form.routeProfile.trim() !== ''
}

export function getAetherHydrationIdentity(
  channelId: number,
  integration?: AetherIntegration
): AetherHydrationIdentity {
  return {
    channelId,
    configRevision: integration?.config_revision ?? null,
  }
}

export function shouldHydrateAetherDraft(
  hydrated: AetherHydrationIdentity | null,
  next: AetherHydrationIdentity,
  dirty: boolean
): boolean {
  if (!hydrated || hydrated.channelId !== next.channelId) return true
  if (hydrated.configRevision === next.configRevision) return false
  return !dirty
}

export function createAetherHydratedState(
  channelId: number,
  integration?: AetherIntegration
): {
  identity: AetherHydrationIdentity
  persisted: AetherIntegration | undefined
  form: AetherIntegrationForm
} {
  const form = integration
    ? {
        instanceID: integration.instance_id,
        routeProfile: integration.route_profile,
        executionMode: integration.execution_mode,
        enabled: integration.enabled,
        capabilityVersion: integration.capability_version,
        controlSecret: '',
        relaySigningSecret: '',
        baseRevision: integration.config_revision,
      }
    : { ...AETHER_INITIAL_FORM }

  return {
    identity: getAetherHydrationIdentity(channelId, integration),
    persisted: integration,
    form,
  }
}

export function isAetherDraftDirty(
  form: AetherIntegrationForm,
  persisted?: AetherIntegration
): boolean {
  if (form.controlSecret !== '' || form.relaySigningSecret !== '') return true

  const baseline = createAetherHydratedState(
    persisted?.channel_id ?? 0,
    persisted
  ).form
  return (
    form.instanceID !== baseline.instanceID ||
    form.routeProfile !== baseline.routeProfile ||
    form.executionMode !== baseline.executionMode ||
    form.enabled !== baseline.enabled ||
    form.capabilityVersion !== baseline.capabilityVersion
  )
}

export function isAetherSharedConfigDirty(
  form: AetherIntegrationForm,
  persisted?: AetherIntegration
): boolean {
  if (!persisted) return false

  return (
    form.routeProfile.trim() !== persisted.route_profile ||
    form.executionMode !== persisted.execution_mode ||
    form.enabled !== persisted.enabled ||
    form.capabilityVersion.trim() !== persisted.capability_version
  )
}

export function canSyncAetherIntegration(state: {
  disabled: boolean
  hydrated: boolean
  hasExistingIntegration: boolean
  credentialsReady: boolean
  dirty: boolean
  pending: boolean
  hasConflict: boolean
}): boolean {
  return (
    !state.disabled &&
    state.hydrated &&
    state.hasExistingIntegration &&
    state.credentialsReady &&
    !state.dirty &&
    !state.pending &&
    !state.hasConflict
  )
}

export function hasActiveAetherCredentialTransition(
  integration:
    | Pick<
        AetherIntegration,
        | 'has_transition_control_secret'
        | 'has_transition_relay_signing_secret'
        | 'transition_secrets_expire_at'
      >
    | undefined,
  nowUnix = Math.floor(Date.now() / 1000)
): boolean {
  return Boolean(
    integration?.has_transition_control_secret &&
    integration.has_transition_relay_signing_secret &&
    Number.isFinite(integration.transition_secrets_expire_at) &&
    integration.transition_secrets_expire_at > nowUnix
  )
}

export function canRevokeAetherCredentialTransition(state: {
  disabled: boolean
  hydrated: boolean
  hasExistingIntegration: boolean
  transitionActive: boolean
  dirty: boolean
  pending: boolean
  hasConflict: boolean
}): boolean {
  return (
    !state.disabled &&
    state.hydrated &&
    state.hasExistingIntegration &&
    state.transitionActive &&
    !state.dirty &&
    !state.pending &&
    !state.hasConflict
  )
}

export function getAetherSyncStatus(
  integration:
    | Pick<
        AetherIntegration,
        'config_revision' | 'remote_config_revision' | 'last_sync_time'
      >
    | undefined,
  hasRemoteConflict: boolean
): AetherSyncStatus {
  if (hasRemoteConflict) return 'conflict'
  if (!integration) return 'not_synchronized'
  if (integration.config_revision !== integration.remote_config_revision) {
    return 'out_of_sync'
  }
  return integration.last_sync_time > 0 ? 'synchronized' : 'not_synchronized'
}

export function persistedAetherConfigSnapshot(
  integration: AetherIntegration
): AetherConfigSnapshot {
  return {
    route_profile: integration.route_profile,
    execution_mode: integration.execution_mode,
    enabled: integration.enabled,
    capability_version: integration.capability_version,
  }
}

export function areAetherFieldsLocked(state: {
  disabled: boolean
  hydrated: boolean
  pending: boolean
  hasConflict: boolean
}): boolean {
  return state.disabled || !state.hydrated || state.pending || state.hasConflict
}

export function applyAetherConflictRetry(
  form: AetherIntegrationForm,
  draft: AetherConfigSnapshot,
  currentRevision: number
): AetherIntegrationForm {
  return {
    ...form,
    routeProfile: draft.route_profile,
    executionMode: draft.execution_mode,
    enabled: draft.enabled,
    capabilityVersion: draft.capability_version,
    baseRevision: currentRevision,
  }
}
