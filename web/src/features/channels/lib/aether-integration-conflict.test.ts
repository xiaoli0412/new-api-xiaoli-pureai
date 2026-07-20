import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { buildAetherConflictDetails } from './aether-integration-conflict'

const draft = {
  route_profile: 'balanced',
  execution_mode: 'direct_channel' as const,
  enabled: true,
  capability_version: '0.1.0',
}

describe('AETHER configuration conflict details', () => {
  test('builds field differences from a stale local revision response', () => {
    const result = buildAetherConflictDetails(
      {
        current_revision: 7,
        current: {
          route_profile: 'economy',
          execution_mode: 'disabled',
          enabled: false,
          capability_version: '0.1.1',
        },
      },
      draft
    )

    assert.deepEqual(result, {
      currentRevision: 7,
      currentConfig: {
        route_profile: 'economy',
        execution_mode: 'disabled',
        enabled: false,
        capability_version: '0.1.1',
      },
      fieldDiff: [
        {
          field: 'route_profile',
          requested: 'balanced',
          current: 'economy',
        },
        {
          field: 'execution_mode',
          requested: 'direct_channel',
          current: 'disabled',
        },
        { field: 'enabled', requested: true, current: false },
        {
          field: 'capability_version',
          requested: '0.1.0',
          current: '0.1.1',
        },
      ],
    })
  })

  test('accepts the remote current_config shape and omits unchanged fields', () => {
    const result = buildAetherConflictDetails(
      {
        current_revision: 11,
        current_config: {
          instance_id: 'aether-primary',
          route_profile: 'premium',
          execution_mode: 'direct_channel',
          enabled: true,
          capability_version: '0.1.0',
          base_revision: 11,
          updated_at: '2026-07-15T08:00:00Z',
        },
        diff: {
          route_profile: {
            requested: 'balanced',
            current: 'premium',
          },
        },
      },
      draft
    )

    assert.deepEqual(result, {
      currentRevision: 11,
      currentConfig: {
        route_profile: 'premium',
        execution_mode: 'direct_channel',
        enabled: true,
        capability_version: '0.1.0',
      },
      fieldDiff: [
        {
          field: 'route_profile',
          requested: 'balanced',
          current: 'premium',
        },
      ],
    })
  })

  test('keeps the revision visible when the server omits current config', () => {
    assert.deepEqual(
      buildAetherConflictDetails({ current_revision: 4 }, draft),
      {
        currentRevision: 4,
        currentConfig: {},
        fieldDiff: [],
      }
    )
  })

  test('uses the server diff when current_config omits a changed field', () => {
    assert.deepEqual(
      buildAetherConflictDetails(
        {
          current_revision: 5,
          current_config: {},
          diff: {
            enabled: {
              requested: true,
              current: false,
            },
          },
        },
        draft
      ),
      {
        currentRevision: 5,
        currentConfig: { enabled: false },
        fieldDiff: [{ field: 'enabled', requested: true, current: false }],
      }
    )
  })

  test('rejects non-conflict payloads without a valid revision', () => {
    assert.equal(buildAetherConflictDetails({}, draft), null)
    assert.equal(
      buildAetherConflictDetails({ current_revision: -1 }, draft),
      null
    )
  })
})
