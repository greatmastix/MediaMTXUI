# Forwarding to other platforms

Forwarding (also called restreaming) sends your stream on to Twitch, YouTube, Kick or any other RTMP, SRT, RTSP or
WHIP server while you stream here. You upload once; the server does the sending. This page shows how to set it up,
where the platform's key goes, and what the status messages mean. It is for streamers and for whoever manages a
stream.

## Short version

On the stream's page, under **Forwarding**: **Add a platform**, choose the **Platform**, paste its **Stream key**
(and **Server** for Kick or Custom), leave **Switch it on now** ticked, click **Add**. Start streaming here; the
forward starts with it.

## Before you start

- **Who can do it**: the stream's owner, operators and admins (everyone who manages the stream).
- **Nothing is re-encoded.** The platform gets exactly what your encoder sends here. So send what the platform
  accepts: for Twitch and YouTube that is H.264 video with AAC audio, which is what OBS sends over RTMP or SRT. The
  stream page's presets for **Twitch** and **YouTube** show suitable settings (see
  [Encoders](encoders.md#recommended-obs-settings)); with one of them picked, the health check warns when the
  platform cannot play what you send (for example Opus audio, which OBS sends over WHIP).
- **Your upload stays the same**, but the server's upload grows by one copy of the stream per forward.
- Check each platform's rules on streaming to several places at once.

## Add a platform

1. On the stream's page, find **Forwarding** and click **Add a platform**.
2. Choose the **Platform**:

   | Platform | What you paste | Where to find it |
   |---|---|---|
   | Twitch | **Stream key** | Twitch Creator Dashboard → Settings → Stream → Primary Stream key |
   | YouTube | **Stream key** | YouTube Studio → Go live → Stream → Stream key |
   | Kick | **Server** and **Stream key** | Kick Creator Dashboard → Settings → Stream URL & Key: paste both |
   | Custom | **Server** and, depending on the protocol, **Stream key** | the other server's documentation |

   Twitch and YouTube need only the key: the server already knows their ingest addresses
   (`rtmp://live.twitch.tv/app` and `rtmps://a.rtmps.youtube.com/live2`). For Kick, paste the RTMPS address it shows;
   if it has no application part, `/app` is added.
3. Paste the **Stream key**. It is a password field: it is sent to the server once and never shown again.
4. Leave **Switch it on now** ticked to start forwarding whenever the stream runs, or untick it to add the forward
   switched off.
5. Click **Add**.

The forward appears in the list with the platform's name and its address without the key (for example
`rtmp://live.twitch.tv`), and a status.

### Custom servers: where the key goes

**Custom** takes an `rtmp://`, `rtmps://`, `srt://`, `rtsp://`, `rtsps://`, `whip://` or `whips://` address.

| Protocol | Server | Stream key field |
|---|---|---|
| RTMP, RTMPS | the server's address with its application, for example `rtmp://host/app` | the stream key |
| WHIP | the WHIP address, for example `whips://host/whip/endpoint` | the bearer token, if the server wants one |
| SRT, RTSP | the full address, credentials included (for SRT: `streamid` and `passphrase` in the address) | leave it empty |

Why it matters: the server keeps keys out of logs where it can. An RTMP key from the key field is sent in the part of
the address that MediaMTX leaves out of its log, and a WHIP token travels as a header. An SRT or RTSP address carries
its credentials in the address itself, and MediaMTX writes forward addresses to its log when a forward starts (without
user name and password, but with the rest). So an SRT `streamid` or `passphrase` ends up in MediaMTX's log, which
admins can read on the **Logs** page. Do not also put an RTMP key into the **Server** address: the form refuses a key
in both places.

## Status

Each forward shows one of these:

| Status | Meaning |
|---|---|
| **Off** | Switched off. Nothing is sent. |
| **On · starts when you go live** | Switched on; nothing is streaming here yet. |
| **Forwarding · 12.3 MB sent** | Sending, with the amount sent so far. |
| **Not getting through** | MediaMTX cannot deliver to the platform. The error is shown below it ("... Check the key; MediaMTX keeps trying every few seconds."). Keys are removed from the message. |
| **On, but not in MediaMTX** | MediaMTX's configuration no longer has this destination (it was edited elsewhere). Switch it off and on again. |
| **On · state unknown** | MediaMTX did not answer just now. |

**Switch off** and **Switch on** toggle a forward without forgetting its key. The bin button, then **Remove**, deletes
it.

## When forwards run

A forward that is switched on runs **whenever the stream is available** in MediaMTX: while your encoder is live,
and also while a [holding screen](holding-screens.md) plays. With a holding screen, the platform keeps receiving the
holding clip between your streams, so the platform's stream does not end when your encoder disconnects. Without a
holding screen, the forward stops when you stop and starts again when you go live.

Changing the holding screen restarts the stream in MediaMTX, so forwards restart too. The page warns you before it
does that.

## Limits and rules

- At most **5 forwards per stream**. The **Add a platform** button disappears at five.
- The same destination with the same key cannot be added twice to one stream.
- A key is at most 512 characters, without spaces.
- The server checks every destination before it uses it. Addresses that point back into the server itself are refused:
  loopback (`localhost`, `127.0.0.1`), the server's own Docker network, link-local addresses and cloud metadata
  services. A host name that cannot be resolved is refused too.

## Where keys are kept

- Keys are stored encrypted and never shown again, not even to admins on the stream's page. To change a key, remove
  the forward and add it again.
- While a forward is switched on, its destination with the key is also in MediaMTX's configuration file (MediaMTX
  reads it from there), which admins of this server can see in **Configuration** and its history.
- Keys never appear in the [audit log](logs-and-audit.md); adding, switching and removing forwards do, with the
  platform and the address without the key.

## Forwards set up in Configuration

Admins can also add forward destinations to any path under **Configuration** (for example **Quick setup → Forward a
stream to another server**). The stream page leaves those alone and does not list them. See
[Configuration](configuration.md).
