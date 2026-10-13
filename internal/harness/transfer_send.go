package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"
)

const maxSendRetries = 5

type sending struct {
	path, abs string
	size      int64
	sent      atomic.Int64
	hashed    atomic.Bool
	done      chan struct{}
	result    map[string]any
	err       error
}

func (s *sending) snapshot() map[string]any {
	return map[string]any{"path": s.path, "resolved_path": s.abs, "size": s.size, "sent": s.sent.Load(), "hashing": !s.hashed.Load(), "running": true}
}

func (t *Transfers) send(ctx context.Context, in Invocation) (Output, error) {
	var a struct {
		Path    string `json:"path"`
		Project string `json:"project"`
		URL     string `json:"upload_url"`
		Yield   *int   `json:"yield_time_ms"`
	}
	if err := Decode(in.Arguments, &a); err != nil {
		return Output{}, err
	}
	u, err := url.Parse(a.URL)
	if err != nil || !t.allowURL(u) {
		return Output{}, &ToolError{"invalid_arguments", "upload_url must be an address returned by receive_file"}
	}
	root, rel, release, err := t.files.resolve(in.Session, a.Project, a.Path)
	if err != nil {
		return Output{}, err
	}
	abs := filepath.Join(root.Name(), rel)
	file, err := openRead(root, rel)
	release()
	if err != nil {
		return Output{}, err
	}
	info, err := file.Stat()
	switch {
	case err != nil:
	case !info.Mode().IsRegular():
		err = errors.New("only regular files can be sent")
	case info.Size() > MaxTransferBytes:
		err = &ToolError{"too_large", "file exceeds 1 GiB"}
	}
	if err != nil {
		file.Close()
		return Output{}, err
	}
	job := &sending{path: a.Path, abs: abs, size: info.Size(), done: make(chan struct{})}
	jobCtx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(job.done)
		defer cancel()
		defer file.Close()
		job.result, job.err = t.upload(jobCtx, u.String(), file, job)
	}()
	yield := 5000
	if a.Yield != nil {
		yield = *a.Yield
	}
	timer := time.NewTimer(time.Duration(yield) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-job.done:
		if job.err != nil {
			return Output{}, job.err
		}
		return Output{Value: job.result}, nil
	case <-ctx.Done():
		cancel()
		<-job.done
		return Output{}, ctx.Err()
	case <-timer.C:
	}
	return Output{
		Value:      job.snapshot(),
		Snapshot:   func() any { return job.snapshot() },
		Completion: func() (any, error) { <-job.done; return job.result, job.err },
		Cancel:     cancel,
	}, nil
}

// upload hashes the file, asks the receiver how much it already has and sends
// the rest in pieces. A dropped connection is retried after asking again, which
// is safe because every piece names its offset.
func (t *Transfers) upload(ctx context.Context, link string, file *os.File, job *sending) (map[string]any, error) {
	h := sha256.New()
	if _, err := io.Copy(h, &ctxReader{ctx, io.NewSectionReader(file, 0, job.size)}); err != nil {
		return nil, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	job.hashed.Store(true)
	var offset int64
	complete, synced, asked, failures := false, false, false, 0
	for !complete {
		var err error
		if synced {
			// With every byte delivered, an empty last piece asks the receiver to
			// finish (it carries the hash); a second time means it will not.
			if offset >= job.size {
				if asked {
					return nil, &ToolError{"protocol_error", "the receiver has every byte but did not confirm the file"}
				}
				asked = true
			}
			offset, complete, err = t.put(ctx, link, file, offset, job, sum)
		} else {
			offset, complete, err = t.head(ctx, link, job.size)
		}
		if err == nil {
			synced = true
			failures = 0
			job.sent.Store(offset)
			continue
		}
		var te *ToolError
		if errors.As(err, &te) || ctx.Err() != nil || failures >= maxSendRetries {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		failures++
		synced = false
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(1<<failures) * 500 * time.Millisecond):
		}
	}
	job.sent.Store(job.size)
	return map[string]any{"path": job.path, "resolved_path": job.abs, "size": job.size, "sent": job.size, "sha256": sum, "done": true}, nil
}

func (t *Transfers) head(ctx context.Context, link string, size int64) (int64, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, link, nil)
	if err != nil {
		return 0, false, err
	}
	resp, err := t.Client.Do(req)
	if err != nil {
		return 0, false, err
	}
	return offsets(resp, size)
}

func (t *Transfers) put(ctx context.Context, link string, file *os.File, offset int64, job *sending, sum string) (int64, bool, error) {
	n := min(t.Chunk, job.size-offset)
	var body io.Reader = http.NoBody
	if n > 0 {
		body = &sendProgress{r: io.NewSectionReader(file, offset, n), job: job, at: offset}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, link, body)
	if err != nil {
		return offset, false, err
	}
	req.ContentLength = n
	req.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	if offset+n == job.size {
		req.Header.Set("Upload-SHA256", sum)
	}
	resp, err := t.Client.Do(req)
	if err != nil {
		return offset, false, err
	}
	return offsets(resp, job.size)
}

// offsets reads the receiver's answer. ToolErrors are final; other errors are retried.
func offsets(resp *http.Response, size int64) (int64, bool, error) {
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK, resp.StatusCode == http.StatusNoContent, resp.StatusCode == http.StatusConflict:
	case resp.StatusCode == http.StatusNotFound:
		return 0, false, &ToolError{"upload_closed", "the upload address is closed or expired; run receive_file on the target again and retry with its new upload_url"}
	case resp.StatusCode == http.StatusUnprocessableEntity:
		var e struct{ Error string }
		json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e)
		return 0, false, &ToolError{"upload_rejected", "the receiver rejected the file: " + e.Error}
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusBadRequest:
		// Cloudflare answers 5xx while the tunnel reconnects; 400 is an interrupted piece.
		return 0, false, fmt.Errorf("the receiver answered %s", resp.Status)
	default:
		return 0, false, &ToolError{"upload_rejected", "the receiver answered " + resp.Status}
	}
	if l := resp.Header.Get("Upload-Length"); l != strconv.FormatInt(size, 10) {
		return 0, false, &ToolError{"size_mismatch", fmt.Sprintf("the receiver expects %s bytes but this file has %d; call receive_file with size=%d", l, size, size)}
	}
	offset, err := strconv.ParseInt(resp.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || offset < 0 || offset > size {
		return 0, false, &ToolError{"protocol_error", "the receiver sent no valid Upload-Offset"}
	}
	return offset, resp.Header.Get("Upload-Complete") == "1", nil
}

type sendProgress struct {
	r   io.Reader
	job *sending
	at  int64
}

func (p *sendProgress) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.at += int64(n)
	p.job.sent.Store(p.at)
	return n, err
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(b)
}
