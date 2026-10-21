---
worth: yes
where: svtlv/torrserver-ownership.js:110
added: 2026-10-21
---
# Share owned torrents between the two TorrServer links

URL 1 will use the user's tailnet address and URL 2 the home LAN IP, both
reaching the same physical TorrServer. The current two-link restoration plan
intentionally treats different normalized addresses as separate server
identities, so a torrent saved through one address is absent from My Torrents
through the other until claimed there too. The user agreed to defer sharing
until after the two-link restoration.

Combine the current user's ownership records for both configured URLs when
filtering My Torrents, intersecting that combined set with the live torrent
list from the active endpoint. A torrent owned through either link should
appear through the other only if it actually exists on that endpoint. This
does not copy torrents between servers or expose torrents owned only by others.

- Saving through a link records ownership for that endpoint as usual.
- Removing a shared entry clears this user's ownership under both configured
  endpoint identities; it must not delete the torrent from TorrServer. Handle
  partial failures without reporting a successful removal that later reappears.
- Preserve account isolation and the equivalent browser-local guest behavior.
- Preserve the upstream two-link UI and routing, shared credential sync, and
  client-local selection with URL 1 as the default.
- Cover shared and different physical servers, torrents missing from one
  endpoint, duplicate hashes, removal failures, and account/URL/selection
  changes while requests are pending.

The ownership API can read records for both URL identities without contacting
the inactive TorrServer address. Prefer reusing existing endpoint identities
and membership operations; physical-server discovery or alias detection is
not required for this behavior.

Related plan: [Restore two TorrServer URLs with a local selector](../plans/20261021-restore-two-torrserver-urls.md).
