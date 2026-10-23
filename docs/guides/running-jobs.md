# Running jobs

```go
client, err := comfy.Dial(ctx, "http://127.0.0.1:8188", nil)
```

One client per ComfyUI server, shared by every job; it is safe for concurrent use. `Dial`
refuses a server older than ComfyUI 0.42 with `comfy.ErrUnsupportedServer`. `comfy.New` makes the
same client without contacting the server, for a backend that may start first: its first `Submit`
does the check, and `client.Ping(ctx)` reports whether the server is up.

## Run, or Submit and Wait

- **`Run(ctx, prompt, opts)`** submits and waits; its context governs the job, as in
  `exec.CommandContext`: when it ends first, Run cancels the job on the server. For a synchronous
  handler.
- **`Submit(ctx, prompt, opts)`** returns a `*Job` once the server queued it; `ctx` bounds the
  submission only. The job runs until it ends or `job.Cancel(ctx)`. `job.Wait(ctx)` waits for the
  outcome; its context bounds only the wait.

A handler that streams a job and wants it cancelled when its request ends says so:

```go
stop := context.AfterFunc(r.Context(), func() { _ = job.Cancel(context.Background()) })
defer stop()
```

## Outcomes

| The job | `Wait` returns |
|---|---|
| succeeded | the result, nil |
| a node raised | the result (outputs that finished first), `*comfy.ExecutionError` |
| cancelled or interrupted, by anyone | the result so far, `comfy.ErrCancelled` |
| its record vanished (a restart) | nil, `comfy.ErrJobLost` |
| the client closed | nil, `comfy.ErrClosed` |
| the server stayed unreachable | nil, the transport error |

`Submit` returns a `*comfy.RejectedError` for a prompt the server refuses (with each node's
errors), and no job. There is no default timeout: a job runs as long as the server runs it.

## Events

```go
for ev := range job.Events(ctx) {
	switch ev := ev.(type) {
	case comfy.ProgressEvent: // ev.Node, ev.Value of ev.Max
	case comfy.PreviewEvent:  // ev.MIME, ev.Data
	case comfy.OutputEvent:   // a node finished
	}
}
```

Events start from the submission; a slow reader gets the latest progress and preview of each
node rather than all of them. Previews come only with `SubmitOptions{Previews: comfy.PreviewAuto}`
(or a server started with `--preview-method`); `SaveImageWebsocket` images always arrive as
previews.

## Results and files

`res.Files(workflows.Txt2ImgSave)` lists a node's files; `client.Open(ctx, file.FileRef)` streams
one; `client.FileURL(ref)` is its `/view` URL. `res.Output(node)` has a node's text and raw UI
output, with the server's metadata (sizes, `animated`). A node inside a loop (`StartLoop`) runs
once per iteration: `Files` lists every iteration's files in order, `Output` is the last run's,
and `res.All()` yields each run.
