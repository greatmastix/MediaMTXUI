import { CircleAlert, CircleCheck } from 'lucide-react'

export function SavedNote({ message, warn }: { message: string | null; warn?: boolean }) {
  if (!message) return null
  const Icon = warn ? CircleAlert : CircleCheck
  return (
    <p role="status" className="flex items-center gap-2 text-sm" data-testid="saved-note">
      <Icon
        className={warn ? 'size-4 shrink-0 text-warning' : 'size-4 shrink-0 text-good'}
        aria-hidden
      />
      {message}
    </p>
  )
}
