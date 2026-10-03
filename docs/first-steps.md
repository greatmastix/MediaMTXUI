# First steps

You have finished the setup wizard and are signed in as the first admin. This page shows you around the UI, then
takes you through the first things to do: create a stream, send video to it from OBS, watch it and share the watch
link, invite a second admin, and set a backup passphrase. It takes about fifteen minutes.

## A tour of the UI

![The dashboard](screenshots/dashboard.png)

The menu on the left (behind the menu button on a phone) lists the pages. Which ones you see depends on your role;
as an admin you see all of them:

| Page | What it is for |
|---|---|
| **Dashboard** | The server at a glance: whether MediaMTX is running, the paths online, the audience and traffic, live and over time. |
| **Streams** | Your streams, one card each, live or offline. Each stream has its own page with its server address and stream key, a preview and its settings. Start here. |
| **Watch** | Watch streams in the browser, one at a time or in a 2 × 2 or 3 × 3 grid, and save layouts by name. |
| **Paths** | Every path MediaMTX knows right now, with their details. A *path* is MediaMTX's word for a stream's name, such as `live/main`. |
| **Recordings** | What MediaMTX recorded, per path, on a timeline: play, download a stretch, delete. |
| **Connections** | Who is connected over which protocol right now, with the details of each connection. |
| **Credentials** | Stream credentials besides the streams' own keys, each limited to what it may do, where, from where and until when. |
| **People** | Who can sign in, with their role; invitations. |
| **Configuration** | MediaMTX's settings: **Quick setup** for the common tasks, every setting in forms, a **YAML** editor and the **History** of every change. |
| **Audit log** | Every change, by whom, from where and when. |
| **Logs** | MediaMTX's log and the sidecar's own recent lines, live and searchable. |
| **Backups** | The backup passphrase and schedule, the kept backups, and restoring. |
| **Account** | Your password, authenticator app, passkeys and where you are signed in. |

An **Exposure** page appears as well when [exposure control](exposure-control.md) is switched on.

Along the top:

- **Streaming / Server**: two views of the same UI. *Server* shows everything. *Streaming* shows only Streams, Watch
  and Account, for when you just want to stream. Each browser remembers its choice. Streamers always get the
  Streaming view.
- The live indicator: **Live** while the page receives updates from the server, **Reconnecting…** if the connection
  drops, **MediaMTX down** if MediaMTX is not answering.
- The theme menu (dark, light or as the system), and **Sign out**.

Roles, briefly: an **admin** can do everything; an **operator** also manages streams and connections; a **viewer**
watches everything and changes nothing; a **streamer** sees only the streams they own. See [People](people.md).

## Switch on the protocols you need

Encoders send video to MediaMTX over a protocol: **RTMP** for OBS and most streaming software, **SRT** for unstable
links, **RTSP** for cameras. If you ticked the ones you need in the setup wizard, skip this. Otherwise:

1. Open **Configuration** → **Quick setup** → **Choose the protocols to serve**.
2. Tick **RTMP** (and any other you need). **HLS** and **WebRTC**, for watching in the browser, are on already; leave
   them on.
3. The page lists the changes, for example "Switch the RTMP server on." Press **Save**.

Make sure the matching port is open in your firewall ([Before you start](before-you-start.md#ports)): 1935/tcp for
RTMP, 8890/udp for SRT, 8554/tcp for RTSP.

## Create your first stream

1. Open **Streams** and press **New stream**.
2. Fill in:
   - **Path**: the stream's name in MediaMTX, which becomes part of every address, for example `live/main`. Letters,
     digits and slashes are a safe choice.
   - **Title**: what people see, for example "Sunday service".
   - **Owner**: leave **Nobody (admins and operators manage it)** for now. To hand a stream to a streamer, invite them
     on the **People** page first, then pick them here.
3. Press **Create stream**.

The stream's page opens. It has its own server address and stream key, and it is **public**: anyone with its watch
link can watch it (only watch, never stream). You can make it private under **Settings** at the bottom of the page by
unticking **Public**; see [Streams](streams.md).

![A stream's page](screenshots/stream.png)

## Stream to it from OBS

1. On the stream's page, in **Go live**, press **Show stream settings**. You see the server address and stream key
   for each protocol that is switched on. Press **Show key** to see the key itself; the copy buttons copy it either
   way.

   > [!WARNING]
   > The stream key is like a password: anyone who has it can stream to this stream. If it gets out, press
   > **New key**; the old one stops working at once.

2. Under **Encoder settings**, pick **Where is the stream headed?**. The page then shows the OBS settings that suit
   it, and later checks your stream against them while you are live.
3. In OBS, open **Settings** → **Stream**, choose Service **Custom...**, and paste the **RTMP** **Server** and
   **Stream key** from the stream's page. Set **Output** and **Video** as the table on the stream's page suggests.
4. Click **Start Streaming** in OBS. Within a few seconds the stream's page turns **Live**, the **Preview** shows your
   video, and **How the stream is doing** appears with the checks.

If OBS cannot connect, check that RTMP is switched on and port 1935/tcp is open, and look at the **Logs** page. The
[Encoders](encoders.md) page has OBS in detail, plus ffmpeg, cameras, SRT and WHIP, and
[Troubleshooting](troubleshooting.md) the usual problems.

> [!NOTE]
> OBS's default x264 settings send B-frames, which browsers cannot play over WebRTC. The player then falls back to
> HLS, a few seconds behind, and the stream's page tells you. To avoid it, set B-frames to 0 in OBS (the **Low
> latency** choice under **Encoder settings** lists it with the other settings for that).

## Watch it and share the watch link

- **On the stream's page**: the **Preview** plays the stream.
- **For everyone else**: in the **Watch** section of the stream's page, the **Watch link** (it looks like
  `https://your-domain/s/live/main`) opens a page that plays the stream in any browser, without an account. Copy it
  with the button next to it and share it. When the stream is offline, the page says so and starts playing by itself
  when you go live.
- **For people with an account**: the **Watch** page plays any stream, and shows several side by side in a grid.
- **For players and other software** (VLC, VRChat, an OBS media source): the same **Watch** section lists addresses
  that need no key, for each protocol that is on.

A private stream has no watch link; people then need an account or the playback key ([Watching](watching.md)).

## Invite a second admin

Do this now, even if you are the only one running the server. If the only admin loses their password, it can only be
reset with a command on the server (`docker compose exec sidecar /mtxui reset-password --username NAME`); a second
admin can do it from the **People** page with one click.

1. Open **People** and press **Invite**.
2. Enter a **Username** and pick the **Role** **Admin: everything**.
3. Press **Invite**. If you signed in more than an hour ago, the UI first asks you to **Confirm it's you** with your
   password (or a code from your authenticator app, or a passkey); admin-level changes such as this one ask for it.
4. The page shows a **join code**, once only. Copy it and send it to the person, ideally another way than the
   address (say, the address by e-mail and the code by phone or chat). It works once, for 72 hours.
5. They open `https://your-domain/join`, enter the code and choose their own password.

The same way you invite streamers, operators and viewers. See [People](people.md).

## Set a backup passphrase

Backups start only once you set a passphrase. After that, a backup is made every night and whenever you press
**Back up now**, encrypted with the passphrase. It holds people, streams and their keys, the configuration and its
history, holding clips, chart history and the audit log, but not the recordings.

1. Open **Backups**.
2. Under **Passphrase**, enter a passphrase of at least 12 characters, and again under **Again**. Press **Set passphrase**.
3. Keep the passphrase somewhere safe, such as a password manager. Without it, nobody can open a backup, this server
   included.
4. A **Schedule** section appears: by default a backup every day at 03:30 UTC, keeping the newest 7. Under **Kept backups**, press
   **Back up now** to make the first one, and download it to keep a copy off the server.

See [Backups](backups.md) for restoring, and for how many backups are kept.

## Secure your own account

On the **Account** page, switch on an **Authenticator app** (a second factor: a six-digit code from an app on your
phone at every sign-in), and save the recovery codes it shows. On an HTTPS install you can also add **Passkeys**, to
sign in with your device's fingerprint, face or PIN instead of a password. See [People](people.md).

## Where to go next

- [Streams](streams.md): keys, public and private streams, guest keys, owners.
- [Forwarding](forwarding.md): send a stream on to YouTube, Twitch or another server.
- [Holding screens](holding-screens.md): a picture or clip that plays while nobody is streaming.
- [Recordings](recordings.md): switch **Record this stream** on, and manage the disk space.
- [Configuration](configuration.md): everything else MediaMTX can do.
- [Upgrading](upgrading.md): keep the server up to date.
