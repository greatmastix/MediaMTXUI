import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

// Vitest runs without globals, so Testing Library cannot register its own cleanup.
afterEach(() => {
  cleanup()
})

// jsdom has no matchMedia; the theme follows a system that prefers light.
Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    addListener: () => undefined,
    removeListener: () => undefined,
    dispatchEvent: () => false,
  }),
})

// jsdom has no media playback: play() returns undefined instead of a promise, and there are no playback statistics.
// The Player falls back to HLS here (no RTCPeerConnection either) and uses both.
Object.defineProperty(HTMLMediaElement.prototype, 'play', {
  writable: true,
  value: () => Promise.resolve(),
})
Object.defineProperty(HTMLVideoElement.prototype, 'getVideoPlaybackQuality', {
  writable: true,
  value: () => ({ totalVideoFrames: 0, droppedVideoFrames: 0 }),
})
