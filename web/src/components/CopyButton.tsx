import { Check, Copy } from 'lucide-react'
import { useState } from 'react'

import { Button } from '@/components/ui/button'

/** Copies text to the clipboard, with a tick for a moment afterwards. */
export function CopyButton({ text, label }: { text: string; label: string }) {
  const [done, setDone] = useState(false)
  return (
    <Button
      type="button"
      size="icon-sm"
      variant="ghost"
      aria-label={`Copy ${label}`}
      onClick={() => {
        void navigator.clipboard.writeText(text).then(() => {
          setDone(true)
          setTimeout(() => {
            setDone(false)
          }, 1500)
        })
      }}
    >
      {done ? <Check className="text-good" /> : <Copy />}
    </Button>
  )
}
