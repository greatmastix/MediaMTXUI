import { useEffect, useState } from 'react'

/** The current time in milliseconds, updated every intervalMs, for relative times and chart windows. */
export function useNow(intervalMs = 1000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => {
      setNow(Date.now())
    }, intervalMs)
    return () => {
      clearInterval(t)
    }
  }, [intervalMs])
  return now
}
