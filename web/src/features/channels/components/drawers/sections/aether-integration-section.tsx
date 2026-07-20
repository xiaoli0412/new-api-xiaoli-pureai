import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { isAxiosError } from 'axios'
import {
  Activity,
  GitCompareArrows,
  HeartPulse,
  RefreshCw,
  Save,
  ShieldCheck,
  ShieldOff,
  TriangleAlert,
  Wifi,
} from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  SideDrawerSection,
  SideDrawerSectionHeader,
} from '@/components/drawer-layout'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { cn } from '@/lib/utils'

import {
  getAetherIntegration,
  syncAetherIntegration,
  upsertAetherIntegration,
} from '../../../api'
import {
  buildAetherConflictDetails,
  type AetherConfigSnapshot,
  type AetherConflictDetails,
} from '../../../lib/aether-integration-conflict'
import {
  AETHER_INITIAL_FORM,
  applyAetherConflictRetry,
  areAetherFieldsLocked,
  canRevokeAetherCredentialTransition,
  canSyncAetherIntegration,
  createAetherHydratedState,
  getAetherHydrationIdentity,
  getAetherSyncStatus,
  hasActiveAetherCredentialTransition,
  hasRequiredAetherConfiguration,
  isAetherDraftDirty,
  isAetherSharedConfigDirty,
  persistedAetherConfigSnapshot,
  shouldHydrateAetherDraft,
  type AetherHydrationIdentity,
  type AetherIntegrationForm,
} from '../../../lib/aether-integration-state'
import type {
  AetherIntegration,
  AetherIntegrationResponse,
  AetherIntegrationUpdate,
} from '../../../types'
import { AetherIntegrationConflictAlert } from './aether-integration-conflict-alert'

type AetherIntegrationSectionProps = {
  channelId: number
  disabled: boolean
  onBusyChange?: (busy: boolean) => void
}

type AetherConflictNotice = {
  source: 'local' | 'remote'
  details: AetherConflictDetails
  draft: AetherConfigSnapshot
}

type AetherSaveRequest = {
  form: AetherIntegrationForm
  draft: AetherConfigSnapshot
  secretTransitionSeconds?: number
}

type AetherRevokeRequest = {
  baseRevision: number
  draft: AetherConfigSnapshot
}

const AETHER_DEFAULT_SECRET_TRANSITION_SECONDS = 5 * 60
const AETHER_MAX_SECRET_TRANSITION_SECONDS = 60 * 60

function aetherConfigSnapshot(
  form: AetherIntegrationForm
): AetherConfigSnapshot {
  return {
    route_profile: form.routeProfile.trim(),
    execution_mode: form.executionMode,
    enabled: form.enabled,
    capability_version: form.capabilityVersion.trim(),
  }
}

function formatAetherTimestamp(timestamp: number): string {
  if (!Number.isFinite(timestamp) || timestamp <= 0) return ''
  return new Date(timestamp * 1000).toLocaleString()
}

function aetherRequestErrorMessage(error: unknown): string | undefined {
  if (isAxiosError<{ message?: string }>(error)) {
    return error.response?.data?.message || error.message
  }
  return error instanceof Error ? error.message : undefined
}

export function AetherIntegrationSection(props: AetherIntegrationSectionProps) {
  const { t } = useTranslation()
  const { onBusyChange } = props
  const queryClient = useQueryClient()
  const [form, setForm] = useState<AetherIntegrationForm>(() => ({
    ...AETHER_INITIAL_FORM,
  }))
  const [conflict, setConflict] = useState<AetherConflictNotice | null>(null)
  const [secretTransitionSeconds, setSecretTransitionSeconds] = useState(
    AETHER_DEFAULT_SECRET_TRANSITION_SECONDS
  )
  const hydratedIdentityRef = useRef<AetherHydrationIdentity | null>(null)
  const persistedIntegrationRef = useRef<AetherIntegration | undefined>(
    undefined
  )
  const integrationQueryKey = ['channel', props.channelId, 'aether'] as const
  const integrationQuery = useQuery({
    queryKey: integrationQueryKey,
    queryFn: () => getAetherIntegration(props.channelId),
  })
  const integration = integrationQuery.data?.data
  const nextHydrationIdentity = getAetherHydrationIdentity(
    props.channelId,
    integration
  )
  const draftDirty = isAetherDraftDirty(form, persistedIntegrationRef.current)
  const hasHydratedChannel =
    hydratedIdentityRef.current?.channelId === props.channelId
  const hasHydratedCurrentRevision =
    hasHydratedChannel &&
    hydratedIdentityRef.current?.configRevision ===
      nextHydrationIdentity.configRevision

  const hydrateIntegration = useCallback(
    (nextIntegration?: AetherIntegration) => {
      const hydrated = createAetherHydratedState(
        props.channelId,
        nextIntegration
      )
      hydratedIdentityRef.current = hydrated.identity
      persistedIntegrationRef.current = hydrated.persisted
      setForm(hydrated.form)
    },
    [props.channelId]
  )

  useEffect(() => {
    if (
      !integrationQuery.isSuccess ||
      !shouldHydrateAetherDraft(
        hydratedIdentityRef.current,
        nextHydrationIdentity,
        draftDirty
      )
    ) {
      return
    }
    hydrateIntegration(integration)
  }, [
    draftDirty,
    hydrateIntegration,
    integration,
    integrationQuery.isSuccess,
    nextHydrationIdentity,
  ])

  useEffect(() => {
    setConflict(null)
    setSecretTransitionSeconds(AETHER_DEFAULT_SECRET_TRANSITION_SECONDS)
  }, [props.channelId])

  const saveMutation = useMutation({
    mutationFn: async (request: AetherSaveRequest) => {
      const { draft, form: requestForm } = request
      const payload: AetherIntegrationUpdate = {
        base_revision: requestForm.baseRevision,
        instance_id: requestForm.instanceID.trim() || undefined,
        route_profile: draft.route_profile,
        execution_mode: draft.execution_mode,
        enabled: draft.enabled,
        capability_version: draft.capability_version || undefined,
      }
      if (requestForm.controlSecret || requestForm.relaySigningSecret) {
        payload.control_secret = requestForm.controlSecret
        payload.relay_signing_secret = requestForm.relaySigningSecret
        if (request.secretTransitionSeconds !== undefined) {
          payload.secret_transition_seconds = request.secretTransitionSeconds
        }
      }
      const response = await upsertAetherIntegration(props.channelId, payload)
      if (response.conflict) {
        const details = buildAetherConflictDetails(response.conflict, draft)
        if (details) {
          return {
            kind: 'conflict' as const,
            conflict: { source: 'local' as const, details, draft },
          }
        }
      }
      if (!response.success || !response.data) {
        throw new Error(
          response.message || t('Failed to save AETHER integration')
        )
      }
      return { kind: 'saved' as const, integration: response.data }
    },
    onSuccess: (result) => {
      if (result.kind === 'conflict') {
        setConflict(result.conflict)
        toast.error(t('Configuration revision conflict'))
        return
      }
      setConflict(null)
      hydrateIntegration(result.integration)
      queryClient.setQueryData<AetherIntegrationResponse>(integrationQueryKey, {
        success: true,
        data: result.integration,
      })
      queryClient.invalidateQueries({
        queryKey: integrationQueryKey,
      })
      toast.success(t('AETHER integration saved'))
    },
    onError: (error: unknown) => {
      const message = aetherRequestErrorMessage(error)
      toast.error(message || t('Failed to save AETHER integration'))
    },
  })

  const syncMutation = useMutation({
    mutationFn: async (draft: AetherConfigSnapshot) => {
      const response = await syncAetherIntegration(props.channelId)
      if (response.conflict) {
        const details = buildAetherConflictDetails(response.conflict, draft)
        if (details) {
          return {
            kind: 'conflict' as const,
            conflict: { source: 'remote' as const, details, draft },
          }
        }
      }
      if (!response.success || !response.data) {
        throw new Error(
          response.message || t('Failed to synchronize AETHER integration')
        )
      }
      return { kind: 'synced' as const, integration: response.data }
    },
    onSuccess: (result) => {
      if (result.kind === 'conflict') {
        setConflict(result.conflict)
        toast.error(t('AETHER synchronization conflict'))
        return
      }
      setConflict(null)
      hydrateIntegration(result.integration)
      queryClient.setQueryData<AetherIntegrationResponse>(integrationQueryKey, {
        success: true,
        data: result.integration,
      })
      queryClient.invalidateQueries({
        queryKey: integrationQueryKey,
      })
      toast.success(t('AETHER integration synchronized'))
    },
    onError: (error: unknown) => {
      const message = aetherRequestErrorMessage(error)
      toast.error(message || t('Failed to synchronize AETHER integration'))
    },
  })

  const revokeMutation = useMutation({
    mutationFn: async (request: AetherRevokeRequest) => {
      const response = await upsertAetherIntegration(props.channelId, {
        base_revision: request.baseRevision,
        revoke_transition_secrets: true,
      })
      if (response.conflict) {
        const details = buildAetherConflictDetails(
          response.conflict,
          request.draft
        )
        if (details) {
          return {
            kind: 'conflict' as const,
            conflict: {
              source: 'local' as const,
              details,
              draft: request.draft,
            },
          }
        }
      }
      if (!response.success || !response.data) {
        throw new Error(
          response.message || t('Failed to revoke previous credentials')
        )
      }
      return { kind: 'revoked' as const, integration: response.data }
    },
    onSuccess: (result) => {
      if (result.kind === 'conflict') {
        setConflict(result.conflict)
        toast.error(t('Configuration revision conflict'))
        return
      }
      setConflict(null)
      hydrateIntegration(result.integration)
      queryClient.setQueryData<AetherIntegrationResponse>(integrationQueryKey, {
        success: true,
        data: result.integration,
      })
      queryClient.invalidateQueries({
        queryKey: integrationQueryKey,
      })
      toast.success(t('Previous credentials were revoked'))
    },
    onError: (error: unknown) => {
      const message = aetherRequestErrorMessage(error)
      toast.error(message || t('Failed to revoke previous credentials'))
    },
  })

  const mutationPending =
    saveMutation.isPending || syncMutation.isPending || revokeMutation.isPending

  useEffect(() => {
    onBusyChange?.(mutationPending)
    return () => {
      if (mutationPending) onBusyChange?.(false)
    }
  }, [mutationPending, onBusyChange])

  const hasExistingIntegration = Boolean(integration)
  const credentialsReady = Boolean(
    integration?.has_control_secret && integration.has_relay_signing_secret
  )
  const rotationRequested = Boolean(
    form.controlSecret || form.relaySigningSecret
  )
  const credentialTransitionActive =
    hasActiveAetherCredentialTransition(integration)
  const requiresSecrets = !credentialsReady
  const secretsIncomplete =
    Boolean(form.controlSecret) !== Boolean(form.relaySigningSecret)
  const rotationWithSharedConfigChanges =
    hasExistingIntegration &&
    rotationRequested &&
    isAetherSharedConfigDirty(form, persistedIntegrationRef.current)
  const transitionDurationInvalid =
    hasExistingIntegration &&
    rotationRequested &&
    (!Number.isInteger(secretTransitionSeconds) ||
      secretTransitionSeconds <= 0 ||
      secretTransitionSeconds > AETHER_MAX_SECRET_TRANSITION_SECONDS)
  const cannotSave =
    props.disabled ||
    !hasHydratedChannel ||
    mutationPending ||
    integrationQuery.isPending ||
    integrationQuery.isError ||
    conflict !== null ||
    !hasRequiredAetherConfiguration(form) ||
    (requiresSecrets && (!form.controlSecret || !form.relaySigningSecret)) ||
    secretsIncomplete ||
    rotationWithSharedConfigChanges ||
    transitionDurationInvalid
  const cannotSync = !canSyncAetherIntegration({
    disabled: props.disabled,
    hydrated: hasHydratedCurrentRevision,
    hasExistingIntegration,
    credentialsReady,
    dirty: draftDirty,
    pending: mutationPending,
    hasConflict: conflict !== null,
  })
  const cannotRevoke = !canRevokeAetherCredentialTransition({
    disabled: props.disabled,
    hydrated: hasHydratedCurrentRevision,
    hasExistingIntegration,
    transitionActive: credentialTransitionActive,
    dirty: draftDirty,
    pending: mutationPending,
    hasConflict: conflict !== null,
  })
  const fieldsLocked = areAetherFieldsLocked({
    disabled: props.disabled,
    hydrated: hasHydratedChannel,
    pending: mutationPending,
    hasConflict: conflict !== null,
  })
  const routeProfileInvalid = !form.routeProfile.trim()

  let connectionLabel = t('Not configured')
  let connectionBadgeClassName =
    'border-border bg-muted/50 text-muted-foreground'
  if (integrationQuery.isPending) {
    connectionLabel = t('Checking')
  } else if (integrationQuery.isError) {
    connectionLabel = t('Unavailable')
    connectionBadgeClassName =
      'border-destructive/30 bg-destructive/10 text-destructive'
  } else if (hasExistingIntegration && !credentialsReady) {
    connectionLabel = t('Credentials incomplete')
    connectionBadgeClassName = 'border-warning/30 bg-warning/10 text-warning'
  } else if (integration?.last_health_status === 'healthy') {
    connectionLabel = t('Connected')
    connectionBadgeClassName = 'border-success/30 bg-success/10 text-success'
  } else if (hasExistingIntegration) {
    connectionLabel = t('Configured')
    connectionBadgeClassName = 'border-info/30 bg-info/10 text-info'
  }

  let healthLabel = t('Not checked')
  let healthBadgeClassName = 'border-border bg-muted/50 text-muted-foreground'
  if (integration?.last_health_status === 'healthy') {
    healthLabel = t('Healthy')
    healthBadgeClassName = 'border-success/30 bg-success/10 text-success'
  } else if (integration?.last_health_status === 'unhealthy') {
    healthLabel = t('Unhealthy')
    healthBadgeClassName =
      'border-destructive/30 bg-destructive/10 text-destructive'
  }

  let syncLabel = t('Not synchronized')
  let syncBadgeClassName = 'border-border bg-muted/50 text-muted-foreground'
  const syncStatus = getAetherSyncStatus(
    integration,
    conflict?.source === 'remote'
  )
  if (syncStatus === 'conflict') {
    syncLabel = t('Conflict')
    syncBadgeClassName =
      'border-destructive/30 bg-destructive/10 text-destructive'
  } else if (syncStatus === 'out_of_sync') {
    syncBadgeClassName = 'border-warning/30 bg-warning/10 text-warning'
  } else if (syncStatus === 'synchronized') {
    syncLabel = t('Last sync succeeded')
    syncBadgeClassName = 'border-success/30 bg-success/10 text-success'
  }

  let secretMessage = t(
    'Both secrets are required when creating the integration.'
  )
  if (hasExistingIntegration && credentialsReady) {
    secretMessage = t('Secrets are stored and never displayed again.')
  } else if (hasExistingIntegration) {
    secretMessage = t('Both secrets must be set together before connecting.')
  }

  const handleUseCurrentConfiguration = () => {
    if (!conflict || conflict.source !== 'local') return
    const currentConfig = conflict.details.currentConfig
    setForm((current) => ({
      ...current,
      routeProfile: currentConfig.route_profile ?? current.routeProfile,
      executionMode: currentConfig.execution_mode ?? current.executionMode,
      enabled: currentConfig.enabled ?? current.enabled,
      capabilityVersion:
        currentConfig.capability_version ?? current.capabilityVersion,
      baseRevision: conflict.details.currentRevision,
    }))
    setConflict(null)
  }

  const handleKeepDraft = () => {
    if (!conflict || conflict.source !== 'local') return
    setForm((current) =>
      applyAetherConflictRetry(
        current,
        conflict.draft,
        conflict.details.currentRevision
      )
    )
    setConflict(null)
  }

  return (
    <SideDrawerSection>
      <SideDrawerSectionHeader
        title={t('AETHER Integration')}
        description={t(
          'Connection, route profile, and synchronized runtime state.'
        )}
        icon={<Activity className='h-4 w-4' aria-hidden='true' />}
        iconTone='info'
      />
      <div
        className='divide-border grid divide-y border-y sm:grid-cols-3 sm:divide-x sm:divide-y-0'
        aria-live='polite'
      >
        <div className='min-w-0 space-y-1.5 px-4 py-3'>
          <div className='text-muted-foreground flex items-center gap-1.5 text-xs font-medium'>
            <Wifi className='size-3.5' aria-hidden='true' />
            <span>{t('Connection')}</span>
          </div>
          <Badge
            variant='outline'
            className={cn(
              'h-auto min-h-5 max-w-full text-center whitespace-normal',
              connectionBadgeClassName
            )}
          >
            {connectionLabel}
          </Badge>
          <p className='text-muted-foreground text-xs break-words'>
            {integration?.instance_id ||
              t('No AETHER integration is configured.')}
          </p>
        </div>
        <div className='min-w-0 space-y-1.5 px-4 py-3'>
          <div className='text-muted-foreground flex items-center gap-1.5 text-xs font-medium'>
            <HeartPulse className='size-3.5' aria-hidden='true' />
            <span>{t('Health')}</span>
          </div>
          <Badge
            variant='outline'
            className={cn(
              'h-auto min-h-5 max-w-full text-center whitespace-normal',
              healthBadgeClassName
            )}
          >
            {healthLabel}
          </Badge>
          <p className='text-muted-foreground text-xs break-words'>
            {integration?.last_health_time
              ? t('Last health check: {{time}}', {
                  time: formatAetherTimestamp(integration.last_health_time),
                })
              : t('No health check recorded')}
          </p>
        </div>
        <div className='min-w-0 space-y-1.5 px-4 py-3'>
          <div className='text-muted-foreground flex items-center gap-1.5 text-xs font-medium'>
            <GitCompareArrows className='size-3.5' aria-hidden='true' />
            <span>{t('Configuration Sync')}</span>
          </div>
          <Badge
            variant='outline'
            className={cn(
              'h-auto min-h-5 max-w-full text-center whitespace-normal',
              syncBadgeClassName
            )}
          >
            {syncLabel}
          </Badge>
          <p className='text-muted-foreground text-xs break-words'>
            {t('Local revision')}:{' '}
            {integration?.config_revision ?? t('Not available')}
            {' / '}
            {t('Remote revision')}:{' '}
            {integration?.remote_config_revision ?? t('Not available')}
          </p>
          <p className='text-muted-foreground text-xs break-words'>
            {integration?.last_sync_time
              ? t('Last synchronized: {{time}}', {
                  time: formatAetherTimestamp(integration.last_sync_time),
                })
              : t('Never synchronized')}
          </p>
        </div>
      </div>

      {integrationQuery.isError && (
        <Alert variant='destructive'>
          <TriangleAlert aria-hidden='true' />
          <AlertTitle>{t('Unable to load AETHER integration')}</AlertTitle>
          <AlertDescription>
            {aetherRequestErrorMessage(integrationQuery.error) ||
              t('Request failed')}
          </AlertDescription>
        </Alert>
      )}

      {conflict && (
        <AetherIntegrationConflictAlert
          source={conflict.source}
          details={conflict.details}
          draft={conflict.draft}
          disabled={props.disabled || mutationPending}
          onUseCurrent={handleUseCurrentConfiguration}
          onKeepDraft={handleKeepDraft}
          onDismiss={() => setConflict(null)}
        />
      )}

      {rotationWithSharedConfigChanges && (
        <Alert variant='destructive'>
          <TriangleAlert aria-hidden='true' />
          <AlertTitle>
            {t('Credential rotation requires a clean configuration')}
          </AlertTitle>
          <AlertDescription>
            {t(
              'Save shared configuration changes before rotating AETHER credentials.'
            )}
          </AlertDescription>
        </Alert>
      )}

      <fieldset
        disabled={fieldsLocked}
        className='space-y-4 disabled:opacity-60'
      >
        <div className='grid gap-4 sm:grid-cols-2'>
          <label className='space-y-1.5'>
            <span className='text-sm font-medium'>
              {t('AETHER Instance ID')}
            </span>
            <Input
              value={form.instanceID}
              disabled={hasExistingIntegration}
              onChange={(event) =>
                setForm((current) => ({
                  ...current,
                  instanceID: event.target.value,
                }))
              }
            />
          </label>
          <label className='space-y-1.5'>
            <span className='text-sm font-medium'>{t('Route Profile')}</span>
            <Input
              value={form.routeProfile}
              required
              aria-invalid={routeProfileInvalid || undefined}
              aria-describedby={
                routeProfileInvalid ? 'aether-route-profile-error' : undefined
              }
              onChange={(event) =>
                setForm((current) => ({
                  ...current,
                  routeProfile: event.target.value,
                }))
              }
            />
            {routeProfileInvalid && (
              <p
                id='aether-route-profile-error'
                className='text-destructive text-xs'
                role='alert'
              >
                {t('Route profile is required.')}
              </p>
            )}
          </label>
          <label className='space-y-1.5'>
            <span className='text-sm font-medium'>{t('Execution Mode')}</span>
            <Select
              value={form.executionMode}
              onValueChange={(value) =>
                setForm((current) => ({
                  ...current,
                  executionMode: value as 'direct_channel' | 'disabled',
                }))
              }
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value='direct_channel'>
                  {t('Direct Channel')}
                </SelectItem>
                <SelectItem value='disabled'>{t('Disabled')}</SelectItem>
              </SelectContent>
            </Select>
          </label>
          <label className='space-y-1.5'>
            <span className='text-sm font-medium'>
              {t('Capability Version')}
            </span>
            <Input
              value={form.capabilityVersion}
              onChange={(event) =>
                setForm((current) => ({
                  ...current,
                  capabilityVersion: event.target.value,
                }))
              }
            />
          </label>
        </div>

        <div className='divide-border border-y'>
          <div className='flex items-center justify-between gap-3 px-4 py-3'>
            <div>
              <p className='text-sm font-medium'>{t('Integration Enabled')}</p>
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Allow AETHER to receive signed relay context and read analysis data.'
                )}
              </p>
            </div>
            <Switch
              aria-label={t('Integration Enabled')}
              checked={form.enabled}
              onCheckedChange={(enabled) =>
                setForm((current) => ({ ...current, enabled }))
              }
            />
          </div>
        </div>

        <div className='grid gap-4 sm:grid-cols-2'>
          <label className='space-y-1.5'>
            <span className='text-sm font-medium'>{t('Control Secret')}</span>
            <Input
              type='password'
              autoComplete='new-password'
              value={form.controlSecret}
              onChange={(event) =>
                setForm((current) => ({
                  ...current,
                  controlSecret: event.target.value,
                }))
              }
            />
          </label>
          <label className='space-y-1.5'>
            <span className='text-sm font-medium'>
              {t('Relay Signing Secret')}
            </span>
            <Input
              type='password'
              autoComplete='new-password'
              value={form.relaySigningSecret}
              onChange={(event) =>
                setForm((current) => ({
                  ...current,
                  relaySigningSecret: event.target.value,
                }))
              }
            />
          </label>
        </div>

        {hasExistingIntegration && rotationRequested && (
          <label className='block space-y-1.5'>
            <span className='text-sm font-medium'>
              {t('Credential transition (seconds)')}
            </span>
            <Input
              type='number'
              min={1}
              max={AETHER_MAX_SECRET_TRANSITION_SECONDS}
              step={1}
              value={secretTransitionSeconds}
              onChange={(event) => {
                const nextValue = Number(event.target.value)
                setSecretTransitionSeconds(
                  Number.isFinite(nextValue) ? nextValue : 0
                )
              }}
            />
            <p className='text-muted-foreground text-xs'>
              {t(
                'Previous credentials remain valid for {{seconds}} seconds after rotation.',
                { seconds: secretTransitionSeconds }
              )}
            </p>
          </label>
        )}

        <div className='text-muted-foreground flex flex-wrap items-center gap-x-4 gap-y-1 text-xs'>
          <span className='inline-flex items-center gap-1'>
            <ShieldCheck className='size-3.5' aria-hidden='true' />
            {secretMessage}
          </span>
        </div>

        {credentialTransitionActive && (
          <div className='border-border/60 flex flex-wrap items-center justify-between gap-3 border-y px-4 py-3'>
            <div className='text-muted-foreground inline-flex min-w-0 items-center gap-1.5 text-xs'>
              <ShieldCheck className='size-3.5 shrink-0' aria-hidden='true' />
              <span className='break-words'>
                {t('Previous credentials remain valid until {{time}}.', {
                  time: formatAetherTimestamp(
                    integration?.transition_secrets_expire_at ?? 0
                  ),
                })}
              </span>
            </div>
            <Button
              type='button'
              size='sm'
              variant='outline'
              disabled={cannotRevoke}
              onClick={() => {
                const persistedIntegration = persistedIntegrationRef.current
                if (!persistedIntegration) return
                revokeMutation.mutate({
                  baseRevision: persistedIntegration.config_revision,
                  draft: persistedAetherConfigSnapshot(persistedIntegration),
                })
              }}
            >
              <ShieldOff className='mr-2 size-4' aria-hidden='true' />
              {t('Revoke previous credentials')}
            </Button>
          </div>
        )}

        <div className='flex flex-wrap gap-2'>
          <Button
            type='button'
            size='sm'
            disabled={cannotSave}
            onClick={() =>
              saveMutation.mutate({
                form: { ...form },
                draft: aetherConfigSnapshot(form),
                secretTransitionSeconds:
                  hasExistingIntegration && rotationRequested
                    ? secretTransitionSeconds
                    : undefined,
              })
            }
          >
            <Save className='mr-2 size-4' aria-hidden='true' />
            {t('Save AETHER Integration')}
          </Button>
          <Button
            type='button'
            size='sm'
            variant='outline'
            disabled={cannotSync}
            onClick={() => {
              const persistedIntegration = persistedIntegrationRef.current
              if (!persistedIntegration) return
              syncMutation.mutate(
                persistedAetherConfigSnapshot(persistedIntegration)
              )
            }}
          >
            <RefreshCw
              className={`mr-2 size-4 ${syncMutation.isPending ? 'animate-spin' : ''}`}
              aria-hidden='true'
            />
            {t('Sync AETHER')}
          </Button>
        </div>
      </fieldset>
    </SideDrawerSection>
  )
}
