# Deploying

## What ships

Production is plain Go: the generated package embeds its templates. Bun, TypeScript and the
workflows project stay on the development machine and in CI; nothing in the backend's image
needs them.

## Keeping generated code current

- Run `go generate ./...` after changing a workflow or regenerating the schema, and commit the
  result. In CI, regenerate and fail on a diff (`git diff --exit-code`).
- `tscomfy check --url <server> --out workflows/comfy/local` tells you when the server's nodes no
  longer match the schema the workflows were written against.
- The generator and `@tscomfy/go` must speak the same template format: install the
  `@tscomfy/go` version that matches the Go module. A mismatch stops `go generate` and says so.

## The client in a service

- One client for the backend's lifetime: `Dial` (or `New`) at start-up, `Close` at shutdown.
  `Close` stops the client's goroutines and requests; jobs still running on the server keep
  running.
- A backend that may start before ComfyUI makes its client with `New`, which contacts nothing:
  the first `Submit` checks the server and connects. `Ping` reads `/system_stats` without a
  socket, for a health check or a status page. The client survives ComfyUI restarts: a dropped socket reconnects while jobs are open (about two minutes of attempts), and
  the next `Submit` reopens it otherwise. A job whose record a restart wiped ends with
  `comfy.ErrJobLost`.
- The client pings the socket while jobs are open, so a connection a proxy dropped silently is
  found within about 40 seconds.
- Behind a proxy that needs a token, put it in `Options.Header`, or in the URL's query, which the
  client keeps on every request and never shows in error messages.
- There is no default job timeout: give `Run` or `Wait` a context with a deadline if a job may
  hang (a stuck custom node).
