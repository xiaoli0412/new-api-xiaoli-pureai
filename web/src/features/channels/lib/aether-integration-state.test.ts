import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import type { AetherIntegration } from '../types'
import {
  AETHER_INITIAL_FORM,
  applyAetherConflictRetry,
  areAetherFieldsLocked,
  canRevokeAetherCredentialTransition,
  canSyncAetherIntegration,
  createAetherHydratedState,
  getAetherSyncStatus,
  hasActiveAetherCredentialTransition,
  getAetherHydrationIdentity,
  hasRequiredAetherConfiguration,
  isAetherDraftDirty,
  persistedAetherConfigSnapshot,
  shouldHydrateAetherDraft,
  type AetherIntegrationForm,
} from './aether-integration-state'

const integration: AetherIntegration = {
  id: 4,
  channel_id: 59,
  instance_id: 'aether-primary',
  route_profile: 'balanced',
  execution_mode: 'direct_channel',
  enabled: true,
  capability_version: '0.1.0',
  config_revision: 6,
  remote_config_revision: 6,
  has_control_secret: true,
  has_relay_signing_secret: true,
  has_transition_control_secret: false,
  has_transition_relay_signing_secret: false,
  transition_secrets_expire_at: 0,
  last_sync_time: 0,
  last_health_time: 0,
  last_health_status: '',
}

function cleanForm(): AetherIntegrationForm {
  return createAetherHydratedState(59, integration).form
}

describe('AETHER draft hydration', () => {
  test('allows initial hydration', () => {
    const next = getAetherHydrationIdentity(59, integration)

    assert.equal(shouldHydrateAetherDraft(null, next, false), true)
  })

  test('ignores a refetch at the same revision', () => {
    const hydrated = getAetherHydrationIdentity(59, integration)

    assert.equal(shouldHydrateAetherDraft(hydrated, hydrated, false), false)
  })

  test('does not overwrite a dirty draft when a newer revision arrives', () => {
    const hydrated = getAetherHydrationIdentity(59, integration)
    const newer = getAetherHydrationIdentity(59, {
      ...integration,
      config_revision: 7,
    })

    assert.equal(shouldHydrateAetherDraft(hydrated, newer, true), false)
  })

  test('always hydrates when the channel changes', () => {
    const hydrated = getAetherHydrationIdentity(59, integration)
    const nextChannel = getAetherHydrationIdentity(60, {
      ...integration,
      channel_id: 60,
    })

    assert.equal(shouldHydrateAetherDraft(hydrated, nextChannel, true), true)
  })

  test('mutation success produces a clean form at the returned revision', () => {
    const result = createAetherHydratedState(59, {
      ...integration,
      route_profile: 'premium',
      config_revision: 7,
    })

    assert.deepEqual(result.identity, {
      channelId: 59,
      configRevision: 7,
    })
    assert.deepEqual(result.form, {
      instanceID: 'aether-primary',
      routeProfile: 'premium',
      executionMode: 'direct_channel',
      enabled: true,
      capabilityVersion: '0.1.0',
      controlSecret: '',
      relaySigningSecret: '',
      baseRevision: 7,
    })
    assert.equal(isAetherDraftDirty(result.form, result.persisted), false)
  })

  test('represents an unconfigured integration as a clean initial form', () => {
    const result = createAetherHydratedState(59, undefined)

    assert.deepEqual(result.form, AETHER_INITIAL_FORM)
    assert.equal(isAetherDraftDirty(result.form, result.persisted), false)
  })
})

describe('AETHER draft safety', () => {
  test('requires a non-blank instance ID and route profile before saving', () => {
    const form = cleanForm()

    assert.equal(hasRequiredAetherConfiguration(form), true)
    assert.equal(
      hasRequiredAetherConfiguration({ ...form, instanceID: '   ' }),
      false
    )
    assert.equal(
      hasRequiredAetherConfiguration({ ...form, routeProfile: '\t' }),
      false
    )
  })

  test('treats a config edit or either secret as dirty', () => {
    const form = cleanForm()

    assert.equal(isAetherDraftDirty(form, integration), false)
    assert.equal(
      isAetherDraftDirty({ ...form, routeProfile: 'premium' }, integration),
      true
    )
    assert.equal(
      isAetherDraftDirty({ ...form, controlSecret: 'control' }, integration),
      true
    )
    assert.equal(
      isAetherDraftDirty(
        { ...form, relaySigningSecret: 'signing' },
        integration
      ),
      true
    )
  })

  test('only allows Sync for a hydrated pristine integration', () => {
    const ready = {
      disabled: false,
      hydrated: true,
      hasExistingIntegration: true,
      credentialsReady: true,
      dirty: false,
      pending: false,
      hasConflict: false,
    }

    assert.equal(canSyncAetherIntegration(ready), true)
    assert.equal(canSyncAetherIntegration({ ...ready, dirty: true }), false)
    assert.equal(canSyncAetherIntegration({ ...ready, pending: true }), false)
    assert.equal(
      canSyncAetherIntegration({ ...ready, hasConflict: true }),
      false
    )
    assert.equal(canSyncAetherIntegration({ ...ready, hydrated: false }), false)
  })

  test('only enables credential revocation for an active pristine transition', () => {
    const transition = {
      ...integration,
      has_transition_control_secret: true,
      has_transition_relay_signing_secret: true,
      transition_secrets_expire_at: 1_700_000_300,
    } as AetherIntegration
    const ready = {
      disabled: false,
      hydrated: true,
      hasExistingIntegration: true,
      transitionActive: hasActiveAetherCredentialTransition(
        transition,
        1_700_000_000
      ),
      dirty: false,
      pending: false,
      hasConflict: false,
    }

    assert.equal(canRevokeAetherCredentialTransition(ready), true)
    assert.equal(
      canRevokeAetherCredentialTransition({ ...ready, dirty: true }),
      false
    )
    assert.equal(
      canRevokeAetherCredentialTransition({
        ...ready,
        transitionActive: false,
      }),
      false
    )
    assert.equal(
      hasActiveAetherCredentialTransition(
        { ...transition, transition_secrets_expire_at: 1_700_000_000 },
        1_700_000_000
      ),
      false
    )
  })

  test('does not report a prior successful sync as current after revisions diverge', () => {
    assert.equal(
      getAetherSyncStatus(
        {
          ...integration,
          config_revision: 8,
          remote_config_revision: 7,
          last_sync_time: 1_700_000_000,
        },
        false
      ),
      'out_of_sync'
    )
    assert.equal(
      getAetherSyncStatus(
        { ...integration, last_sync_time: 1_700_000_000 },
        false
      ),
      'synchronized'
    )
    assert.equal(getAetherSyncStatus(integration, true), 'conflict')
  })

  test('builds a remote conflict snapshot from persisted integration data', () => {
    const unsavedForm = { ...cleanForm(), routeProfile: 'experimental' }

    assert.deepEqual(persistedAetherConfigSnapshot(integration), {
      route_profile: 'balanced',
      execution_mode: 'direct_channel',
      enabled: true,
      capability_version: '0.1.0',
    })
    assert.notEqual(
      persistedAetherConfigSnapshot(integration).route_profile,
      unsavedForm.routeProfile
    )
  })

  test('locks fields before hydration and while pending or conflicted', () => {
    const editable = {
      disabled: false,
      hydrated: true,
      pending: false,
      hasConflict: false,
    }

    assert.equal(areAetherFieldsLocked(editable), false)
    assert.equal(areAetherFieldsLocked({ ...editable, hydrated: false }), true)
    assert.equal(areAetherFieldsLocked({ ...editable, pending: true }), true)
    assert.equal(
      areAetherFieldsLocked({ ...editable, hasConflict: true }),
      true
    )
  })

  test('restores the captured draft and advances its retry revision', () => {
    const captured = {
      route_profile: 'premium',
      execution_mode: 'disabled' as const,
      enabled: false,
      capability_version: '0.2.0',
    }
    const changedAfterRequest = {
      ...cleanForm(),
      routeProfile: 'changed-after-request',
      executionMode: 'direct_channel' as const,
      enabled: true,
      capabilityVersion: '0.3.0',
      controlSecret: 'control',
      relaySigningSecret: 'signing',
    }

    assert.deepEqual(
      applyAetherConflictRetry(changedAfterRequest, captured, 9),
      {
        ...changedAfterRequest,
        routeProfile: 'premium',
        executionMode: 'disabled',
        enabled: false,
        capabilityVersion: '0.2.0',
        baseRevision: 9,
      }
    )
  })
})
