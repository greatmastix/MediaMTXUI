import { CopyButton } from '@/components/CopyButton'
import { mask, type PublishTarget } from '@/lib/streamAddresses'

/** Address rows for one key: values masked until shown, each with a copy button. */
export function TargetList({
  targets,
  secret,
  shown,
  prefix,
}: {
  targets: PublishTarget[]
  secret: string
  shown: boolean
  prefix: string
}) {
  return (
    <ul className="space-y-3">
      {targets.map((t) => (
        <li key={t.protocol} className="space-y-1.5" data-testid={`${prefix}-${t.protocol}`}>
          <p className="text-sm">
            <span className="font-medium">{t.protocol}</span>{' '}
            <span className="text-muted-foreground">— {t.use}</span>
          </p>
          {t.fields.map((f) => (
            <div key={f.label} className="flex items-center gap-2">
              <span className="w-24 shrink-0 text-xs text-muted-foreground">{f.label}</span>
              <code
                className="min-w-0 flex-1 rounded-md border bg-background px-2 py-1 font-mono text-xs break-all"
                data-testid={`${prefix}-${t.protocol}-${f.label}`}
              >
                {f.secret && !shown ? mask(f.value, secret) : f.value}
              </code>
              <CopyButton text={f.value} label={`${t.protocol} ${f.label.toLowerCase()}`} />
            </div>
          ))}
        </li>
      ))}
    </ul>
  )
}
