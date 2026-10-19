# How Go libraries shape long-running remote jobs

Research for decision D2 (`docs/decisions.md`), done 2026-10-19. Signatures were read from source in the module cache
(`go env GOMODCACHE`) or from GOROOT (Go 1.27.2). The question: how should a Go client expose a
job that queues, runs for seconds to minutes, streams progress, can be cancelled, and ends in
success, rejection, failure or cancellation?

## 1. Start, wait, result

**Temporal** (`go.temporal.io/sdk` v1.50.0, `client/client.go:1160`, `internal/internal_workflow_client.go:119`)

```go
ExecuteWorkflow(ctx context.Context, options StartWorkflowOptions, workflow any, args ...any) (WorkflowRun, error)
type WorkflowRun interface {
	GetID() string
	GetRunID() string
	Get(ctx context.Context, valuePtr any) error
	GetWithOptions(ctx context.Context, valuePtr any, options WorkflowRunGetOptions) error
}
CancelWorkflow(ctx context.Context, workflowID string, runID string) error
```

A cheap handle; a blocking `Get` that fills the result on success "or return[s] corresponding
error". Cancelling is a separate call; `Get`'s context only bounds the polling.

**Google Cloud long-running operations** (`cloud.google.com/go/longrunning` v1.3.0, `longrunning.go:121-292`)

```go
func (op *Operation) Poll(ctx context.Context, resp protoadapt.MessageV1, opts ...gax.CallOption) error
func (op *Operation) Wait(ctx context.Context, resp protoadapt.MessageV1, opts ...gax.CallOption) error
func (op *Operation) Cancel(ctx context.Context, opts ...gax.CallOption) error
func (op *Operation) Done() bool
```

A failed operation and a failed call are both the error; `Done()` tells them apart, which the
documentation has to explain. Generated wrappers type the result:
`func (op *LongRunningRecognizeOperation) Wait(ctx, opts...) (*speechpb.LongRunningRecognizeResponse, error)`.

**Replicate** (`github.com/replicate/replicate-go` v0.26.0)

```go
func (r *Client) Run(ctx context.Context, identifier string, input PredictionInput, webhook *Webhook) (PredictionOutput, error)
func (r *Client) CreatePrediction(ctx context.Context, version string, input PredictionInput, webhook *Webhook, stream bool) (*Prediction, error)
func (r *Client) Wait(ctx context.Context, prediction *Prediction, opts ...WaitOption) error
func (r *Client) WaitAsync(ctx context.Context, prediction *Prediction, opts ...WaitOption) (<-chan *Prediction, <-chan error)
func (r *Client) CancelPrediction(ctx context.Context, id string) (*Prediction, error)
```

Two layers with different failure semantics: `Run` turns a failure into `*ModelError{Prediction}`;
the low-level path leaves it in `Prediction.Status`. From the source (`run.go:96`), `Run` checks
only `prediction.Error != nil`, so a cancelled prediction without an error message would return
`(output, nil)` (read, not tested).

**Docker** (`github.com/docker/docker` v28.5.2, `client/container_wait.go`; `github.com/moby/moby/client` v0.6.2)

```go
func (cli *Client) ContainerWait(ctx context.Context, containerID string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error)
// the rewritten module keeps the two channels, in a struct:
type ContainerWaitResult struct { Result <-chan container.WaitResponse; Error <-chan error }
```

A non-zero exit is data (`WaitResponse.StatusCode`); the error channel is for transport.

**os/exec** (GOROOT `os/exec/exec.go`)

```go
func CommandContext(ctx context.Context, name string, arg ...string) *Cmd
func (c *Cmd) Start() error
func (c *Cmd) Wait() error
func (c *Cmd) Run() error
func (c *Cmd) Output() ([]byte, error)
type ExitError struct { *os.ProcessState; Stderr []byte }
```

The context "is used to interrupt the process". A failed exit is a typed `*ExitError` carrying
partial data, and `Output` still returns what it collected. `Wait` "must not be called
concurrently".

**Kubernetes watch** (`k8s.io/apimachinery` v0.37.1, `pkg/watch/watch.go`)

```go
type Interface interface { Stop(); ResultChan() <-chan Event }
```

One channel and an explicit `Stop` that "must always be called"; errors arrive in-band as events.

**River** (`github.com/riverqueue/river` v0.49.0) uses option structs (`*Config`, `*InsertOpts`);
`Subscribe(kinds ...EventKind) (<-chan *Event, func())` documents that slow consumers lose events.

## 2. Streams

- **gRPC** (`google.golang.org/grpc` v1.84.0): `Recv() (*Res, error)`, `io.EOF` at the end. The
  caller must cancel or drain, or "a goroutine and a context will be leaked".
- **Anthropic and OpenAI SDKs** (`anthropic-sdk-go` v1.82.0, `openai-go/v3` v3.82.0):
  `ssestream.Stream[T]` with `Next() bool`, `Current() T`, `Err() error`, `Close() error`. The
  Anthropic SDK also has `All() iter.Seq[T]`, where breaking out doesn't call `Close` and errors
  need a separate `Err()`; its newer tool runner uses `All(ctx) iter.Seq2[*BetaMessage, error]`.
- **`bufio.Scanner`**: `Scan`, `Text`, `Err`.
- **`net/http/httptrace`**: a struct of optional callbacks, called synchronously.
- **`iter.Seq2[T, error]`** appears in Google Cloud's generated iterators ("the iterator will stop
  after that iteration"). The standard library has no such API; proposals for an iterator error
  type were closed (golang.org/issue/67924).
- **The `iter` package documentation** (GOROOT `iter/iter.go`), on single-use iterators: "Calling
  the iterator again after stopping early may continue the stream, but calling it again after the
  sequence is finished will yield no values at all. Doc comments for functions or methods that
  return single-use iterators should document this fact: `// It returns a single-use iterator.`"
  The Go blog's "Range Over Function Types" (2024-08-20): a push iterator does its cleanup when
  `yield` returns false. Neither says anything about contexts; libraries take one in the method
  that returns the sequence.

## 3. A failed job: error or status

| Library | A failed or cancelled job is |
|---|---|
| Temporal | an error: `*WorkflowExecutionError` wrapping `*ApplicationError`, `*CanceledError`, `*TimeoutError`; `errors.As` |
| GCP operations | an error (`*apierror.APIError`); `Done()` tells it from a transport error |
| os/exec | a typed `*ExitError` with partial data; `Output` returns stdout too |
| Replicate | a status field on the low-level path; `*ModelError{Prediction}` from `Run` |
| Docker | data in `WaitResponse`; the error channel is transport only |
| k8s watch | an in-band error event |

## 4. Cancellation

- Only `os/exec` ties a context to the job's life, because the job is a local child process.
- Temporal, GCP, Replicate, Docker and River bound only the call with the context and cancel the
  remote job with a separate call (`CancelWorkflow`, `op.Cancel`, `CancelPrediction`, `JobCancel`).
  Replicate's `WaitAsync` returns `ctx.Err()` and leaves the prediction running.

## 5. Options

- Structs or struct pointers: `slog.NewJSONHandler(w, *HandlerOptions)`, `net/http.Server` fields,
  `exec.Cmd` fields, `coder/websocket.Dial(ctx, u, *DialOptions)`, Temporal, River, Moby's per-call
  option structs.
- Variadic opaque options: `encoding/json/v2` (`Deterministic(bool)`, `JoinOptions`).
- Functional options: generated SDKs (Stainless's `option.RequestOption`), Replicate, Moby's
  constructor.

## 6. What recurs, and what to avoid

Recurs:

- start returns a cheap handle; a blocking `Wait(ctx)`; a separate `Cancel(ctx)`;
- the wait's context bounds only the wait;
- a job failure is a typed error reached with `errors.As`, carrying the record or partial data;
- streams are pull-based with an explicit end; newer code adds `iter.Seq2[T, error]` adapters;
- a stream that drops events says so;
- option structs in the standard library and infrastructure code.

Avoid:

- pairs of channels: callers select on both, and an abandoned one leaks a goroutine;
- one error for both "the call failed" and "the job failed" with no cheap way to tell them apart;
- two layers with different failure semantics (Replicate's cancel gap);
- an `iter.Seq[T]` with a side-channel `Err()` and no cleanup on `break`;
- dropping events silently;
- a request's context implicitly owning a remote job meant to outlive it.

These shaped D2.
