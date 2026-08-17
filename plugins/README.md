# Dropping in a plugin

Any executable named `plugin-<name>` placed in this folder is started alongside
the bundled ones the next time the stack comes up. There is nothing to register
and nothing to rebuild.

```
plugins/
  plugin-ringlogix        # becomes the plugin "ringlogix"
```

Then `make up`.

## What a plugin has to do

Introduce itself over NATS, using the SDK in `pkg/plugin`. Everything else —
which tools it offers, what settings it needs, whether it is configured once or
once per customer — it declares for itself, and the console builds its settings
form from that declaration. Core never learns anything about it at compile time.

The shortest possible version is `cmd/plugin-echo`.

## What being in this folder does not buy

Trust. A plugin found here is discovered and supervised; every tool it offers
arrives pending, and an administrator has to approve each one in
Settings → Plugins before it can be used. Anything that changes a connected
system additionally needs writes turned on for that plugin and the calling
person's permission at the moment it runs.

Its stdout goes through the same redacting log handler as everything else, so a
plugin that logs carelessly still cannot put a credential on the container's
stdout.

## Building one from this repository

A plugin that lives in the repository does not belong here — put it in
`cmd/plugin-<name>/` and it is built into the image automatically. This folder
is for binaries built elsewhere.

To build one for this folder, target the same platform as the image:

```
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o plugins/plugin-foo ./path/to/foo
```
