import { CircleAlert, CircleCheck, CircleMinus } from 'lucide-react'
import type { ReactNode } from 'react'

import { cn } from '@/lib/utils'

// A state as the server dashboard shows it: a neutral pill with a coloured icon and a word, never colour alone.

const icons = {
  good: { Icon: CircleCheck, className: 'text-good' },
  warning: { Icon: CircleAlert, className: 'text-warning' },
  neutral: { Icon: CircleMinus, className: 'text-muted-foreground' },
}

export function StatusPill({
  tone,
  children,
  ...props
}: { tone: keyof typeof icons; children: ReactNode } & React.ComponentProps<'span'>) {
  const { Icon, className } = icons[tone]
  return (
    <span
      className="inline-flex items-center gap-1.5 rounded-full bg-accent py-0.5 pr-2 pl-1.5 text-xs whitespace-nowrap text-foreground"
      {...props}
    >
      <Icon className={cn('size-3 shrink-0', className)} aria-hidden />
      {children}
    </span>
  )
}
