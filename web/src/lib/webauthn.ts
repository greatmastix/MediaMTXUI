// Passkeys in the browser: the server's WebAuthn options (JSON, binary fields as base64url) turned into what
// navigator.credentials takes, and the authenticator's answer back into JSON for the server. Browsers that have the
// standard converters (parseCreationOptionsFromJSON, toJSON) use them; the rest go through the same steps by hand.

type Json = Record<string, unknown>

const fromB64url = (s: string): ArrayBuffer => {
  const b64 = s
    .replace(/-/g, '+')
    .replace(/_/g, '/')
    .padEnd(Math.ceil(s.length / 4) * 4, '=')
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out.buffer
}

const toB64url = (b: ArrayBuffer | null | undefined): string | undefined => {
  if (!b) return undefined
  let s = ''
  for (const byte of new Uint8Array(b)) s += String.fromCharCode(byte)
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/** Whether this browser, on this page, can use passkeys at all. */
export function passkeysSupported(): boolean {
  return typeof window !== 'undefined' && window.isSecureContext && 'PublicKeyCredential' in window
}

interface Descriptor {
  id: string
  type: string
  transports?: string[]
}

const descriptors = (list: unknown) =>
  ((list as Descriptor[] | undefined) ?? []).map((d) => ({ ...d, id: fromB64url(d.id) }))

function creationOptions(o: Json): PublicKeyCredentialCreationOptions {
  const user = o.user as Json
  return {
    ...(o as unknown as PublicKeyCredentialCreationOptions),
    challenge: fromB64url(o.challenge as string),
    user: {
      ...(user as unknown as PublicKeyCredentialUserEntity),
      id: fromB64url(user.id as string),
    },
    excludeCredentials: descriptors(o.excludeCredentials) as PublicKeyCredentialDescriptor[],
  }
}

function requestOptions(o: Json): PublicKeyCredentialRequestOptions {
  return {
    ...(o as unknown as PublicKeyCredentialRequestOptions),
    challenge: fromB64url(o.challenge as string),
    allowCredentials: descriptors(o.allowCredentials) as PublicKeyCredentialDescriptor[],
  }
}

function credentialJSON(c: PublicKeyCredential): Json {
  const withJSON = c as PublicKeyCredential & { toJSON?: () => unknown }
  if (typeof withJSON.toJSON === 'function') return withJSON.toJSON() as Json
  const r = c.response
  const response: Json = { clientDataJSON: toB64url(r.clientDataJSON) }
  if (r instanceof AuthenticatorAttestationResponse) {
    response.attestationObject = toB64url(r.attestationObject)
    response.transports = r.getTransports()
  } else if (r instanceof AuthenticatorAssertionResponse) {
    response.authenticatorData = toB64url(r.authenticatorData)
    response.signature = toB64url(r.signature)
    response.userHandle = toB64url(r.userHandle)
  }
  return {
    id: c.id,
    rawId: toB64url(c.rawId),
    type: c.type,
    authenticatorAttachment: c.authenticatorAttachment ?? undefined,
    clientExtensionResults: c.getClientExtensionResults(),
    response,
  }
}

/** Creates a passkey from the server's creation options and returns the answer for the server. */
export async function createPasskey(options: Json): Promise<Json> {
  const cred = await navigator.credentials.create({ publicKey: creationOptions(options) })
  if (!(cred instanceof PublicKeyCredential)) throw new Error('No passkey was made.')
  return credentialJSON(cred)
}

/** Signs a challenge with a passkey (any of this site's, or one of allowCredentials) and returns the answer. */
export async function signWithPasskey(options: Json): Promise<Json> {
  const cred = await navigator.credentials.get({ publicKey: requestOptions(options) })
  if (!(cred instanceof PublicKeyCredential)) throw new Error('No passkey was used.')
  return credentialJSON(cred)
}

/** A message for a failed or cancelled passkey prompt. */
export function passkeyError(e: unknown): string {
  if (e instanceof DOMException && (e.name === 'NotAllowedError' || e.name === 'AbortError')) {
    return 'The passkey prompt was cancelled or timed out.'
  }
  if (e instanceof DOMException && e.name === 'InvalidStateError') {
    return 'This passkey is already registered here.'
  }
  return e instanceof Error ? e.message : 'The passkey did not work.'
}
