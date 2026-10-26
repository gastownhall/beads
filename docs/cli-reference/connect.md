---
title: "bd connect"
description: "Attach this workspace to a remote bd serve over HTTP"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc connect`.

Attach this workspace to a remote bd serve over HTTP.

bd connect verifies the server first (a handshake: api_version, wire
revision, and — when --expect-project-id is given — workspace identity) and
writes nothing until that succeeds. On success it writes the per-user
activation sidecar (.beads/http_target.json, never git-tracked) and sets this
workspace's metadata.json to backend "http".

A credential, if the server requires one, is never read from a flag or
recorded on disk by this command. It comes from the same ladder every http
request does: the BEADS_HTTP_TOKEN environment variable, then
BEADS_HTTP_TOKEN_COMMAND (a helper that prints a token), then the credentials
file's [host:port] section, then no credential at all — which the tip OSS
server's loopback-trust posture answers legitimately, not as a failure. Both
variables name the server they are for, host[:port]=value (for example
BEADS_HTTP_TOKEN=bd.example.com=&lt;token&gt;); a bare value is refused, since it
would be sent to whatever server the workspace's http_target.json names.

--allow-plaintext IS recorded on disk, in the sidecar alongside the server
url: once granted here, a credential may cross this one server in the clear
on every later command too, not only this one, without re-passing the flag
or setting BEADS_HTTP_ALLOW_INSECURE=1 yourself. A later connect to a
DIFFERENT url starts that grant over; it is never carried forward to a
server it was not given for.

Examples:
  bd connect http://127.0.0.1:8080                         # loopback, no TLS needed
  bd connect https://bd.example.com --expect-project-id p1 # pin workspace identity
  bd connect https://bd.example.com --ca-file ./ca.pem     # trust only this CA
  bd connect --clear                                       # forget the server, restoring the previous backend


```
bd connect [url] [flags]
```

**Flags:**

```
      --allow-plaintext            allow a plain http connection to a non-loopback host; remembered in the sidecar for every later command against this server
      --ca-file string             trust ONLY this PEM file for this server (resolved and recorded as an absolute path)
      --clear                      remove this workspace's http activation sidecar and restore its previous backend selection in metadata.json
      --expect-project-id string   pin the workspace identity the server must present at every handshake
      --force                      allow switching a workspace that already selects a different backend
```
