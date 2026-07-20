import type { TFunction } from 'i18next'
import { TriangleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

import {
  AETHER_CONFIG_FIELDS,
  type AetherConfigSnapshot,
  type AetherConflictDetails,
} from '../../../lib/aether-integration-conflict'

type AetherIntegrationConflictAlertProps = {
  source: 'local' | 'remote'
  details: AetherConflictDetails
  draft: AetherConfigSnapshot
  disabled: boolean
  onUseCurrent: () => void
  onKeepDraft: () => void
  onDismiss: () => void
}

function formatAetherConfigValue(
  field: keyof AetherConfigSnapshot,
  value: string | boolean,
  t: TFunction
): string {
  if (field === 'enabled') return value ? t('Enabled') : t('Disabled')
  if (field === 'execution_mode') {
    return value === 'direct_channel' ? t('Direct Channel') : t('Disabled')
  }
  return value === '' ? t('Empty') : String(value)
}

export function AetherIntegrationConflictAlert(
  props: AetherIntegrationConflictAlertProps
) {
  const { t } = useTranslation()
  const diffByField = new Map(
    props.details.fieldDiff.map((item) => [item.field, item])
  )
  const currentFields = AETHER_CONFIG_FIELDS.filter(
    (field) => props.details.currentConfig[field] !== undefined
  )
  const fieldLabels: Record<keyof AetherConfigSnapshot, string> = {
    route_profile: t('Route Profile'),
    execution_mode: t('Execution Mode'),
    enabled: t('Integration Enabled'),
    capability_version: t('Capability Version'),
  }

  return (
    <Alert variant='destructive'>
      <TriangleAlert aria-hidden='true' />
      <AlertTitle>{t('Configuration revision conflict')}</AlertTitle>
      <AlertDescription className='space-y-3 text-left'>
        <p>
          {props.source === 'local'
            ? t(
                'This AETHER configuration changed since the editor was opened.'
              )
            : t('AETHER has a newer remote configuration.')}{' '}
          {t('Review the field differences before retrying.')}
        </p>
        <p className='text-foreground text-xs font-medium'>
          {t('Current revision: {{revision}}', {
            revision: props.details.currentRevision,
          })}
        </p>

        {currentFields.length > 0 ? (
          <div className='divide-border divide-y border-y'>
            {currentFields.map((field) => {
              const diff = diffByField.get(field)
              const current = props.details.currentConfig[field]
              if (current === undefined) return null
              const requested = diff?.requested ?? props.draft[field]

              return (
                <div
                  key={field}
                  className='grid gap-2 py-2 sm:grid-cols-3 sm:gap-3'
                >
                  <div className='min-w-0'>
                    <span className='text-muted-foreground block text-[11px] font-medium'>
                      {t('Field')}
                    </span>
                    <span className='text-foreground inline-flex max-w-full items-center gap-1.5 text-xs font-medium'>
                      <span className='truncate'>{fieldLabels[field]}</span>
                      {diff && (
                        <Badge variant='destructive'>{t('Changed')}</Badge>
                      )}
                    </span>
                  </div>
                  <div className='min-w-0'>
                    <span className='text-muted-foreground block text-[11px] font-medium'>
                      {t('Your value')}
                    </span>
                    <span className='text-foreground block text-xs break-words'>
                      {formatAetherConfigValue(field, requested, t)}
                    </span>
                  </div>
                  <div className='min-w-0'>
                    <span className='text-muted-foreground block text-[11px] font-medium'>
                      {t('Current value')}
                    </span>
                    <span className='text-foreground block text-xs break-words'>
                      {formatAetherConfigValue(field, current, t)}
                    </span>
                  </div>
                </div>
              )
            })}
          </div>
        ) : (
          <p className='text-xs'>
            {t('Current configuration was not returned by the server.')}
          </p>
        )}

        <div className='flex flex-wrap gap-2'>
          {props.source === 'local' && currentFields.length > 0 && (
            <Button
              type='button'
              size='sm'
              variant='outline'
              disabled={props.disabled}
              onClick={props.onUseCurrent}
            >
              {t('Use current configuration')}
            </Button>
          )}
          {props.source === 'local' && (
            <Button
              type='button'
              size='sm'
              variant='outline'
              disabled={props.disabled}
              onClick={props.onKeepDraft}
            >
              {t('Keep my changes')}
            </Button>
          )}
          <Button
            type='button'
            size='sm'
            variant='ghost'
            onClick={props.onDismiss}
          >
            {t('Dismiss')}
          </Button>
        </div>
      </AlertDescription>
    </Alert>
  )
}
