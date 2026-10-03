# People and access

This page covers who can sign in to MediaMTX UI and what each person may do: the four roles, inviting people,
resetting passwords, your own account (password, authenticator app, passkeys, sessions), the "Confirm it's you"
prompt, sign-in protection, and stream credentials. Most of it is for admins; the [Your account](#your-account)
section is for everyone.

**Short version for admins:** open **People**, press **Invite**, pick a role and pass on the one-time join code. The
person opens `https://your-domain/join`, enters the code and chooses a password. Codes work once and for 72 hours.
If the only admin is locked out, run this on the server, in the folder with `compose.yaml`:

```bash
docker compose exec sidecar /mtxui reset-password --username NAME
```

## Roles

Every account has one of four roles. Each role can do everything the roles above it in this table can, except that
a streamer's view is limited to the streams it owns.

| Role | Who it is for | What they can do |
|---|---|---|
| **Streamer** | Someone who streams to their own stream(s) | Sees only the streams they own. On those: show and regenerate the keys, make the stream public or private, switch recording on or off, set the holding screen, forwarding and guest keys, disconnect the encoder, watch it. Their own **Account** page. |
| **Viewer** | Someone who watches, a producer, a monitor wall | Watches every stream, public or private, and sees the **Dashboard**, **Streams** (read-only), **Watch**, **Paths** (live state) and **Recordings** (play and download). Changes nothing. |
| **Operator** | A technician who runs shows | Everything a viewer can, plus: manages every stream (as the owner can), sees **Connections** (who is connected, with their addresses) and disconnects clients, and deletes recordings. |
| **Admin** | Whoever runs the server | Everything: creates and deletes streams and picks their owners, **Credentials**, **People**, **Configuration**, **Audit log**, **Logs**, **Backups**, **Exposure** (when exposure control is on), and switches recording back on after the free-space guard stopped it. |

A few details that follow from this:

- Streamers do not get the **Recordings** page, even for their own streams. A viewer, operator or admin can play or
  download them.
- Only admins create or delete streams and choose who owns one. Operators manage existing streams but cannot change
  the owner.
- Viewers, operators and admins have two modes in the top bar: **Streaming** (just streams and watching) and
  **Server** (every page). If you miss a page in the menu, switch to **Server**. Streamers always see the streaming
  mode.
- A role change applies at the person's next request; pages they have open reload.

## Inviting someone

1. Open **People** and press **Invite**.
2. Enter a **Username** (2 to 32 letters, digits, dots, underscores or hyphens, starting with a letter or digit) and
   pick a **Role**. The list explains each role in one line.
3. Press **Invite**. A panel shows the join code, for example `ABCD-EFGH-JKLM-NPQR`, and when it expires.
4. Give the person the code, and tell them to open `https://your-domain/join`. Send the code a different way than
   the address if you can (a chat message and a phone call, say).

The code is shown only this once and works once, for 72 hours. Until the person joins, the **State** column says
**Invited** with the expiry, or **code expired**. Press **New join code** to make another one; the old one stops
working.

On the **Join MediaMTX UI** page the person enters the **Join code** (case, dashes and spaces do not matter), a
**New password** (at least 12 characters, not the username) and the **Password again**, then presses **Join**. They
are signed in straight away.

> [!TIP]
> To give a new person a stream, invite them as a streamer, then press **Edit** next to their name and tick the
> streams under **Owns**. You can also choose the owner on the stream's own page.

## Editing, disabling and deleting

Press **Edit** next to a person to change their **Role** and the streams they **Own**, then **Save**. Below that,
**Sign-in and access** shows how many sessions they have and their second factors, with these buttons:

- **Sign out everywhere** ends all their sessions (for yourself: **Sign out everywhere else**). Open live views close
  within seconds.
- **Disable the account** stops them from signing in and ends their sessions; **Enable the account** undoes it. The
  account, its streams and its history stay.
- **Reset second factor** removes their authenticator app, recovery codes and passkeys, for a lost phone or key. They
  sign in with just their password and can set them up again.

**Delete** (then **Delete NAME** to confirm) removes the account with its sessions and saved layouts. Streams they
owned stay, without an owner.

You cannot change your own role, disable yourself or delete yourself.

### There is always an admin

The last admin cannot be demoted, disabled or deleted: the UI answers "This is the last admin." Make someone else
an admin first. Having two admins is a good idea anyway: either can reset the other's password.

## Resetting a password

Passwords are never shown or set by an admin. Instead, a reset is a join code for an existing account:

1. On **People**, press **Reset password** next to the person.
2. Read the note, then press **Make a reset code for NAME**.
3. Pass on the code. The person opens `/join`, enters it and chooses a new password.

Until the code is used, their old password keeps working. When it is used, all their other sessions end. Be careful
with a reset code: whoever has it can choose a new password and sign in as that person, without their authenticator
app or passkey. If it may have gone astray, make another one; that ends the first.

### When nobody can sign in as an admin

If the only admin forgot the password, or lost the authenticator app and the recovery codes, make the code on the
server instead. Run this in the folder with `compose.yaml` (replace `NAME` with the username):

```bash
docker compose exec sidecar /mtxui reset-password --username NAME
```

You should see something like:

```
Join code for NAME: ABCD-EFGH-JKLM-NPQR
It works once, until 2026-10-06 14:00 UTC. Open the UI's /join page and enter it to choose a new password.
```

Open `https://your-domain/join`, enter the code and choose a new password. This works for any account that is not
disabled, and the audit log records it (as `cli`). Anyone who can run `docker compose` on the server can do this,
which is why access to the server itself matters. Restoring a backup does not help with a lost password: it brings
back the same accounts and passwords.

## Your account

Everyone has an **Account** page, at the bottom of the menu. It shows who you are signed in as and
has these sections.

### Password

Enter your **Current password**, the **New password** and **New password again**, then press **Change password**.
New passwords need at least 12 characters and must not be your username. When it worked, the page says "Changed.
Your other sessions were signed out."

### Authenticator app

Optional. With it on, signing in with your password also asks for a six-digit code from an app on your phone (Google
Authenticator, Microsoft Authenticator, 1Password and similar).

1. Press **Set up an authenticator app** and enter your password, then **Continue**.
2. Scan the QR code with the app, or type in the key shown next to it.
3. Enter **The code it shows** and press **Switch on**.
4. The page shows ten **recovery codes**. Each one signs you in once instead of an app code, if you lose your phone.
   They are shown only now: **Download** or copy them, keep them somewhere safe, then press **I have saved them**.

While it is on, the section shows how many recovery codes are left. **New recovery codes** replaces them (the old ones
stop working) and **Switch off** removes the app; both ask for your password. On the sign-in page, after your
password, enter the **Code from your authenticator app**, or one of your recovery codes.

Lost your phone and your recovery codes? Another admin can press **Reset second factor** on **People**. If you are the
only admin, use the [server command](#when-nobody-can-sign-in-as-an-admin).

### Passkeys

A passkey signs you in with your fingerprint, face or device PIN instead of your password (it counts as both factors
at once), and confirms admin-level changes the same way. To add one, enter a **Name for the new passkey** (Laptop,
Phone, YubiKey…) and **Your password**, press **Add a passkey**, and follow your browser's prompt. You can add up to
ten, and **Rename** or **Remove** each. Your password stays, so removing a passkey never locks you out. On the sign-in
page, press **Sign in with a passkey**.

> [!NOTE]
> Browsers allow passkeys only on an HTTPS address (or `localhost`), with a domain name rather than an IP address.
> The standard install has HTTPS, so passkeys just work. In [local-network mode](install-local.md) the UI runs on
> plain HTTP, so the page says "Passkeys need the UI on an HTTPS address, which this server does not have." Passwords
> and authenticator apps work there. The `MTXUI_PASSKEYS` setting ([Sidecar configuration](config.md)) can also
> switch passkeys off.

### Where you are signed in

Lists every session: browser and system, address, when and how you signed in, and when it was last active. **Sign
out** ends one session; **Sign out everywhere else** ends all but the one you are using.

A session ends after 12 hours without use, and 7 days after sign-in however active you are. Admins can change both
with `MTXUI_SESSION_IDLE_TIMEOUT` and `MTXUI_SESSION_MAX_AGE` (see [Sidecar configuration](config.md)).

## "Confirm it's you"

For admin-level changes, the UI asks admins to prove again who they are when the session last did so more than an
hour ago (signing in counts). A **Confirm it's you** dialog asks for your **Password**, a code from your
authenticator app, or **Use a passkey**. After that the change goes through by itself, and you are not asked again
for an hour. If someone got hold of your signed-in browser, this stops them there.

It is asked before:

- **People**: inviting, new join codes and password resets, changing a role, disabling or enabling, deleting,
  signing someone out everywhere, resetting a second factor.
- **Credentials**: creating and revoking.
- **Configuration**: saving in the **YAML** editor and restoring a version from **History**.
- **Backups**: setting the passphrase, changing the schedule, deleting a backup, restoring.
- **Exposure**: opening and closing ports and changing the automatic rules.
- Switching this confirmation itself off or on.

Everyday changes do not ask: the Quick setup, Paths, Global settings and Path defaults forms, streams, recordings.

To switch it off, an admin unticks **Ask me before admin-level changes** under **Confirming it's you** on the Account
page (which asks you to confirm once more). It is a personal setting: each admin decides for themselves.

## Sign-in protection

The sign-in page slows down guessing without telling an attacker which usernames exist:

| Protection | Default | Setting |
|---|---|---|
| Sign-in attempts per client address and minute | 20 | `MTXUI_LOGIN_RATE_PER_MINUTE` (1 to 600) |
| Failed sign-ins before a username is locked (for that client address) | 5 | `MTXUI_LOCKOUT_THRESHOLD` (1 to 100) |
| How long the lock lasts | 15 minutes | `MTXUI_LOCKOUT_DURATION` (1m to 24h) |

A locked sign-in says "Too many failed sign-ins for this username. Try again in N minutes." Unknown usernames lock the
same way. The lock applies to the address the failures came from, so a stranger guessing your username does not lock
you out from your own computer. Codes and passwords you enter while signed in (the second sign-in step, "Confirm it's
you", the Account page) have a lock of their own per account. Join codes are rate-limited per address too.

These settings, like every sidecar setting, go into a `compose.override.yaml`; [Sidecar configuration](config.md)
explains how.

## Stream credentials

Most of the time you do not need this page: every stream gets its own publish and watch keys on its stream page, and
[guest keys](streams.md) give someone temporary access to one stream. **Credentials** (admins only) are the general
form behind them: a name and secret, or a token, that an encoder or player gives MediaMTX, limited to:

- **May**: **Publish** (send a stream), **Read** (watch), **Playback** (recordings through MediaMTX's playback
  server) and **Metrics** (MediaMTX's metrics).
- **Paths**: names separated by commas, or `~` and a regular expression (`~^studio/`); empty means any path.
- **From**: IP addresses or networks; empty means anywhere.
- **Expires**: **Never**, **In a day**, **In a week**, **In 30 days** or **In a year**.

Use one when the per-stream keys do not fit, for example:

- A path you set up under **Configuration** (a [Quick setup](configuration.md#quick-setup) task, a pulled camera, a
  regular expression path) rather than as a stream: it has no keys of its own, so readers and publishers need a
  credential.
- One login for many paths, such as a recorder or monitor wall that reads every camera under `~^cams/`.
- An encoder that should be accepted only from one address.

Credentials do not give access to MediaMTX's Control API: that stays with the sidecar.

### Creating one

1. Press **New credential**.
2. Enter a **Name**. Clients send it as the user name; it also shows in lists and logs.
3. Pick the **Kind**: **Name and secret** for RTSP, RTMP, SRT and most clients, or **Bearer token** for WebRTC
   clients (WHIP and WHEP, such as OBS's WHIP output).
4. Tick what it **May** do, and fill in **Paths**, **From** and **Expires** as needed.
5. Press **Create**.

The next panel shows the secret once ("Copy the secret now: it is not shown again"), with ready-made addresses that
carry it for each protocol that is on (RTSP, RTMP, SRT, and WHIP or WHEP). Copy what you need, then press **I have
copied it**. The server keeps only a fingerprint of the secret, so a lost secret cannot be shown again: create a new
credential instead.

The list shows each credential's paths, addresses, expiry, when it was last used and its state (active, expired,
revoked). The keys of your streams and their guest keys are listed here too, under generated names (`key-…`,
`view-…`). **Revoke**, then **Revoke NAME**, refuses new connections at once and closes the streams it has open.

> [!NOTE]
> **Playback** and **Metrics** credentials are only useful if those MediaMTX servers are reachable, and the standard
> install never publishes their ports. Inside the UI, recordings and charts work without any credential.

### From the command line

The same can be done on the server, which is handy for scripts. Each command runs in the folder with
`compose.yaml`. Create a credential that may publish to `cam1` (the secret is printed once):

```bash
docker compose exec sidecar /mtxui credential add --name cam1-encoder --actions publish --paths cam1
```

Other options: `--kind token`, `--sources 192.168.1.0/24`, `--expires 720h`, `--json`, and `--secret-stdin` to
supply your own secret (16 to 128 letters, digits and `. _ ~ -`). List every credential:

```bash
docker compose exec sidecar /mtxui credential list
```

Revoke one:

```bash
docker compose exec sidecar /mtxui credential revoke --name cam1-encoder
```

Revoking from the command line refuses new connections; sessions that are already open stay until they end (the
**Revoke** button in the UI also closes them). Commands run here are recorded in the audit log as `cli`.

## Guest keys

A guest key lets someone without an account stream to, or watch, one stream for a limited time, for example a guest
speaker's encoder. They are made on the stream's page by its owner, an operator or an admin, and stop working when
they expire or are revoked. See [Streams](streams.md).

## See also

- [Security](security.md): the full list of what protects sign-in and access.
- [Logs and audit](logs-and-audit.md): every change above is recorded in the audit log.
- [Sidecar configuration](config.md): every setting mentioned here.
