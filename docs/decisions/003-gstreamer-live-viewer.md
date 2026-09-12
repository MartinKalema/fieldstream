# ADR-003: Use GStreamer for normal live viewing

**Status:** Accepted for the trusted local lab; the future permissions interface is proposed
**Date:** 2026-09-13
**Deciders:** Project maintainer, following the live camera trials

## Context

We want to control how long the player waits before showing a picture. The native GStreamer viewer gives us an explicit waiting setting and uses the existing camera stream. It does not change camera compression, recording or uploads.

The observations support further use, but do not prove a consistent speed improvement. At the 100 ms setting, one screenshot measured approximately **0.43 s** for both native and local browser viewing. Another measured **0.22 s native** and **0.36 s browser**. These are individual clock readings, not averages or maximum delays. [Trial record](../gstreamer-viewer.md).

The 20 ms trial showed broken blocks during movement. They disappeared in the user's 100 ms repeat. The user then tried 50 ms and reported no blocks during the same movements. **Camera-to-screen delay at 50 ms has not been measured.** The configured wait is only one part of total delay.

## Decision

Use GStreamer for `./lab view`, with **50 ms** as the normal starting setting and **100 ms** available when more waiting is needed. Keep the bounded `gstreamer_check` diagnostic at its **100 ms reference default**. Both commands share the same pipeline builder and process handling; their starting settings are deliberately different.

Remove the old live browser players, their proxy, buffer experiment and optical measurement code. Disable the receiver's WebRTC listeners. Retain a standalone white elapsed clock for manual GStreamer delay readings and the independent saved-MP4 compression comparison page. Historical browser measurements remain documented; they are not current product features.

## Options considered

| Option | Benefit | Cost and limits |
| --- | --- | --- |
| Keep browser viewing as the normal choice | Existing comparison tools and progress warnings | Less direct control over display waiting |
| Use the native viewer now | Explicit waiting settings; reuses the current stream | Separate runtime and basic window; more platform testing |
| Build a complete viewing interface first | Could combine camera selection, controls and permissions | Larger scope before we have established the playback behaviour |

The native viewer is the selected next step. These local trials do not establish capacity for many viewers or reliability on other machines.

## The interface and permissions still needed

GStreamer displays video. A future interface still needs login, camera selection and permitted actions. Proposed roles could be **viewer** for assigned cameras, **operator** for approved stream controls, and **administrator** for account and permission management. These roles are not implemented.

The current CLI trusts access to the Mac. The local media servers allow anonymous local RTSP reads of registered cameras and local management API access. Removing the browser proxy and listeners does not add individual user authentication. Adding a login screen alone would leave direct access around it.

A future Go service must check the person's permission for both the action and camera. Media access must enforce that decision on every supported playback path; hiding a camera or button is insufficient. Replace the current anonymous local permissions in a separate, tested change, preserve distinct internal service credentials, and decide how permission removal affects an already open stream and offline use. Expose only selected status fields to the interface, never private settings or controller tokens.

## Consequences and next checks

The current native window has a generic OpenGL title. It has no picture-progress warning, automatic clock measurement or automatic reconnection. The clock target adds none of those features. Its process remaining open does not prove that fresh pictures are appearing.

Before the browser removal, Go tests, vet and race checks passed, and a normal live viewer remained open for approximately 138 seconds, beyond the diagnostic's 120-second limit. That check covers process lifetime, not a measured picture-age bound. Repeat the relevant checks after code removal. Longer runs, poor connections and repeated camera-to-screen readings at 50 ms remain needed.

Design and test login, camera permissions and media enforcement together before treating this as a system for separate users. Any future native progress warning and automatic delay measurement need their own display-level evidence; the retired browser tests cannot validate them.
