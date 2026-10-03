import { z } from 'zod'

// The session's CSRF token, sent on every state-changing request. The sidecar returns it on sign-in, on setup and
// from GET /api/v1/session.
let csrfToken = ''

export function setCsrfToken(token: string) {
  csrfToken = token
}

/** The session's CSRF token, for requests that do not go through request() (the WHEP client). */
export function csrfTokenValue() {
  return csrfToken
}

/** An error answer from the sidecar: its machine-readable kind and a sentence meant for people. */
export class ApiError extends Error {
  readonly status: number
  readonly kind: string

  constructor(status: number, kind: string, message: string) {
    super(message)
    this.status = status
    this.kind = kind
  }
}

const errorSchema = z.object({ error: z.string(), message: z.string() })

// Step-up: an admin-level change answered 403 "step_up" waits for the page to have the user confirm
// it is them (the handler the app shell sets: a dialog), then goes again, once.
let stepUpHandler: (() => Promise<boolean>) | null = null

export function setStepUpHandler(h: (() => Promise<boolean>) | null) {
  stepUpHandler = h
}

async function send(method: string, path: string, body?: unknown, signal?: AbortSignal) {
  try {
    return await sendOnce(method, path, body, signal)
  } catch (e) {
    if (e instanceof ApiError && e.kind === 'step_up' && stepUpHandler && (await stepUpHandler())) {
      return sendOnce(method, path, body, signal)
    }
    throw e
  }
}

// A Blob body (a file upload) goes as it is, with its own type; anything else as JSON.
async function sendOnce(method: string, path: string, body?: unknown, signal?: AbortSignal) {
  const headers: Record<string, string> = { Accept: 'application/json' }
  const blob = body instanceof Blob
  if (body !== undefined) headers['Content-Type'] = blob ? body.type : 'application/json'
  if (method !== 'GET' && csrfToken) headers['X-CSRF-Token'] = csrfToken
  const res = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : blob ? body : JSON.stringify(body),
    signal,
    credentials: 'same-origin',
  })
  if (!res.ok) {
    let kind = 'http'
    let message = `The server answered ${res.status}.`
    try {
      const parsed = errorSchema.safeParse(await res.json())
      if (parsed.success) {
        kind = parsed.data.error
        message = parsed.data.message
      }
    } catch {
      // not JSON: keep the generic message
    }
    throw new ApiError(res.status, kind, message)
  }
  return res
}

/** Sends a request and validates the JSON answer against schema. */
export async function request<T>(
  method: string,
  path: string,
  schema: z.ZodType<T>,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const res = await send(method, path, body, signal)
  return schema.parse(await res.json())
}

/** Sends a request whose answer has no body. */
export async function requestNoContent(method: string, path: string, body?: unknown) {
  await send(method, path, body)
}

/**
 * Why the server refuses a GET of path (null: it does not), without reading an answer it accepts: for an address a
 * <video> or a link uses, which shows nothing of the server's message.
 */
export async function refusal(path: string): Promise<ApiError | null> {
  const ctl = new AbortController()
  try {
    await sendOnce('GET', path, undefined, ctl.signal)
    return null
  } catch (e) {
    if (e instanceof ApiError) return e
    throw e
  } finally {
    ctl.abort()
  }
}
