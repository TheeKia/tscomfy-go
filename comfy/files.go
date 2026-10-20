package comfy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
)

// UploadOptions configures Upload. nil means the defaults.
type UploadOptions struct {
	Subfolder string     // a folder under the target directory
	Type      FolderType // the target directory; zero: FolderInput
	Overwrite bool       // replace a file of the same name; otherwise the server may rename
}

// UploadedFile is a file as the server stored it.
type UploadedFile struct {
	FileRef
	// Value is what a loader node's file input takes, such as LoadImage's image: "subfolder/name",
	// with " [temp]" or " [output]" after it outside the input directory. Pass it to a string
	// parameter.
	Value string
}

// File is a file being downloaded. Close it.
type File struct {
	io.ReadCloser
	ContentType string // the server's Content-Type
	Size        int64  // -1 when unknown
}

// Upload streams r to the server as a file named name, through POST /upload/image, for a loader
// node. The stored name can differ from name: without Overwrite, the server renames a file whose
// name is taken by different content (name (1).ext), and reuses the name for identical content.
//
// Upload returns when the request ends or ctx does. Like net/http with a request body, it may
// leave a goroutine in a Read of r that hasn't returned; it ends when that Read does.
func (c *Client) Upload(ctx context.Context, name string, r io.Reader, opts *UploadOptions) (UploadedFile, error) {
	const op = "POST /upload/image"
	if opts == nil {
		opts = &UploadOptions{}
	}
	folder := opts.Type
	if folder == "" {
		folder = FolderInput
	}
	switch {
	case name == "":
		return UploadedFile{}, errors.New("comfy: Upload needs a file name")
	case !folder.valid():
		return UploadedFile{}, fmt.Errorf("comfy: UploadOptions.Type %q is not a folder of the server", opts.Type)
	}
	ctx, cancel, err := c.callContext(ctx)
	if err != nil {
		return UploadedFile{}, err
	}
	defer cancel()

	body, w := io.Pipe()
	form := multipart.NewWriter(w)
	written := make(chan error, 1)
	go func() {
		err := writeForm(form, name, r, folder, opts)
		_ = w.CloseWithError(err)
		written <- err
	}()
	resp, err := c.do(ctx, http.MethodPost, "upload/image", nil, body, form.FormDataContentType())
	// The request is over: the writer ends at its next write, or once a pending Read of r returns.
	_ = body.CloseWithError(errUploadEnded)
	var formErr error
	select {
	case formErr = <-written:
	case <-ctx.Done():
	}
	if err != nil {
		return UploadedFile{}, c.closedOr(err)
	}
	if resp.StatusCode != http.StatusOK {
		return UploadedFile{}, unexpected(resp, op)
	}
	if formErr != nil && !errors.Is(formErr, errUploadEnded) {
		drain(resp)
		return UploadedFile{}, fmt.Errorf("comfy: %s: reading the file: %w", op, formErr)
	}
	var answer struct {
		Name      string     `json:"name"`
		Subfolder string     `json:"subfolder"`
		Type      FolderType `json:"type"`
	}
	if err := decodeJSON(resp, op, &answer); err != nil {
		return UploadedFile{}, err
	}
	if answer.Name == "" || !answer.Type.valid() {
		return UploadedFile{}, &ProtocolError{Op: op, Err: errors.New("the answer has no file name or folder")}
	}
	value := answer.Name
	if answer.Subfolder != "" {
		value = answer.Subfolder + "/" + answer.Name
	}
	if answer.Type != FolderInput {
		value += " [" + string(answer.Type) + "]"
	}
	return UploadedFile{Filename: answer.Name, Subfolder: answer.Subfolder, Type: answer.Type, Value: value}, nil
}

// errUploadEnded stops the form's writer once the request is over.
var errUploadEnded = errors.New("comfy: the upload ended")

func writeForm(form *multipart.Writer, name string, r io.Reader, folder FolderType, opts *UploadOptions) error {
	part, err := form.CreateFormFile("image", name)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, r); err != nil {
		return err
	}
	fields := [][2]string{{"type", string(folder)}}
	if opts.Subfolder != "" {
		fields = append(fields, [2]string{"subfolder", opts.Subfolder})
	}
	if opts.Overwrite {
		fields = append(fields, [2]string{"overwrite", "true"})
	}
	for _, f := range fields {
		if err := form.WriteField(f[0], f[1]); err != nil {
			return err
		}
	}
	return form.Close()
}

// Open downloads a file the server has, such as an output, through GET /view. ctx bounds the
// download, reading included. The caller closes the file. A file the server doesn't have is an
// *HTTPError with StatusCode 404.
func (c *Client) Open(ctx context.Context, ref FileRef) (*File, error) {
	const op = "GET /view"
	ctx, cancel, err := c.callContext(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodGet, "view", viewQuery(ref), nil, "")
	if err != nil {
		cancel()
		return nil, c.closedOr(err)
	}
	if resp.StatusCode != http.StatusOK {
		err := unexpected(resp, op)
		cancel()
		return nil, err
	}
	return &File{
		ReadCloser:  &cancelOnClose{ReadCloser: resp.Body, cancel: cancel},
		ContentType: resp.Header.Get("Content-Type"),
		Size:        resp.ContentLength,
	}, nil
}

// FileURL returns the URL that /view serves a file at, for a page the server allows. It keeps the
// base URL's query, so a token in it is in the result.
func (c *Client) FileURL(ref FileRef) string {
	return endpoint(c.base, "view", viewQuery(ref)).String()
}

func viewQuery(ref FileRef) url.Values {
	return url.Values{"filename": {ref.Filename}, "subfolder": {ref.Subfolder}, "type": {string(ref.Type)}}
}

// callContext is ctx, also cancelled by Close, so Close aborts the call; ErrClosed after Close.
func (c *Client) callContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if c.life.Err() != nil {
		return nil, nil, ErrClosed
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.life, cancel)
	return ctx, func() { stop(); cancel() }, nil
}

// closedOr is ErrClosed when the client closed during the call, err otherwise.
func (c *Client) closedOr(err error) error {
	if c.life.Err() != nil {
		return ErrClosed
	}
	return err
}
