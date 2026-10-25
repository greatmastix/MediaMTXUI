// Limits of holding clips made in the browser, apart from the encoder (@/lib/holdingClip loads mediabunny, only when
// someone makes a clip), so pages can show them without loading it.

/**
 * The longest holding clip made here: a longer video is cut there, a longer recording stretch is not offered. An hour,
 * as uploads may be (holding.go): MediaMTX reads a clip whole on every reload, about 5 s an hour.
 */
export const holdingClipMaxSeconds = 3600
