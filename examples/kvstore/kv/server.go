package kv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Server serves a Store over HTTP:
//
//	PUT    /kv/{key}            body: value             200 {revision, op}
//	POST   /kv/{key}?expect=N   body: value  (CAS)      200 {revision, op} | 409
//	DELETE /kv/{key}                                    200 {revision, op} | 404
//	GET    /kv/{key}                                    200 {entry, revision, op} | 404
//
// Every request is one OTel span ("KV <op>") with the attributes an
// operator would want anyway: kv.key, kv.value, kv.expect, the resulting
// kv.revision, what a read returned (kv.found, kv.read.value,
// kv.read.version), and kv.op, the store's linearization index.
// kv.convert.yaml turns these spans into model steps.
type Server struct {
	Store  *Store
	tracer trace.Tracer
}

// NewServer serves store, emitting spans to tp (nil: none).
func NewServer(store *Store, tp trace.TracerProvider) *Server {
	if tp == nil {
		tp = noop.NewTracerProvider()
	}
	return &Server{Store: store, tracer: tp.Tracer("github.com/casselc/quint-connect-go/examples/kvstore")}
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /kv/{key}", s.put)
	mux.HandleFunc("POST /kv/{key}", s.cas)
	mux.HandleFunc("DELETE /kv/{key}", s.del)
	mux.HandleFunc("GET /kv/{key}", s.get)
	return mux
}

func (s *Server) span(r *http.Request, op string) (context.Context, trace.Span) {
	return s.tracer.Start(r.Context(), "KV "+op, trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attribute.String("kv.key", r.PathValue("key"))))
}

func finish(span trace.Span, w http.ResponseWriter, res Result, err error) {
	span.SetAttributes(attribute.Int64("kv.revision", res.Revision), attribute.Int64("kv.op", res.Op))
	status := http.StatusOK
	switch {
	case IsConflict(err):
		status = http.StatusConflict
	case IsNotFound(err):
		status = http.StatusNotFound
	case err != nil:
		status = http.StatusBadRequest
	}
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		var oe outcomeErr
		if errors.As(err, &oe) {
			span.SetAttributes(attribute.String("kv.outcome", oe.outcome))
		}
	} else {
		span.SetAttributes(attribute.String("kv.outcome", "ok"))
	}
	span.SetAttributes(attribute.Int("http.response.status_code", status))
	span.End()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(res)
}

func body(r *http.Request) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	return string(b), err
}

func (s *Server) put(w http.ResponseWriter, r *http.Request) {
	ctx, span := s.span(r, "Put")
	v, err := body(r)
	if err != nil {
		finish(span, w, Result{}, err)
		return
	}
	span.SetAttributes(attribute.String("kv.value", v))
	finish(span, w, s.Store.Put(ctx, r.PathValue("key"), v), nil)
}

func (s *Server) cas(w http.ResponseWriter, r *http.Request) {
	ctx, span := s.span(r, "CAS")
	v, err := body(r)
	if err != nil {
		finish(span, w, Result{}, err)
		return
	}
	exp, err := strconv.ParseInt(r.URL.Query().Get("expect"), 10, 64)
	if err != nil {
		finish(span, w, Result{}, fmt.Errorf("expect: %w", err))
		return
	}
	span.SetAttributes(attribute.String("kv.value", v), attribute.Int64("kv.expect", exp))
	res, err := s.Store.CAS(ctx, r.PathValue("key"), exp, v)
	finish(span, w, res, err)
}

func (s *Server) del(w http.ResponseWriter, r *http.Request) {
	ctx, span := s.span(r, "Delete")
	res, err := s.Store.Delete(ctx, r.PathValue("key"))
	finish(span, w, res, err)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	ctx, span := s.span(r, "Get")
	res, err := s.Store.Get(ctx, r.PathValue("key"))
	span.SetAttributes(attribute.Bool("kv.found", res.Found), attribute.String("kv.read.value", res.Entry.Value),
		attribute.Int64("kv.read.version", res.Entry.Version))
	// A miss is a successful read for the model: it returned "not found".
	if IsNotFound(err) {
		span.SetAttributes(attribute.Int64("kv.revision", res.Revision), attribute.Int64("kv.op", res.Op), attribute.String("kv.outcome", "ok"))
		span.End()
		w.WriteHeader(http.StatusNotFound)
		return
	}
	finish(span, w, res, err)
}

// Client talks to a Server.
type Client struct {
	Base string // e.g. http://127.0.0.1:8080
	HTTP *http.Client
}

func (c *Client) do(ctx context.Context, method, key, query, value string) (Result, error) {
	u := c.Base + "/kv/" + url.PathEscape(key) + query
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewBufferString(value))
	if err != nil {
		return Result{}, err
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	var res Result
	_ = json.NewDecoder(resp.Body).Decode(&res)
	switch resp.StatusCode {
	case http.StatusOK:
		return res, nil
	case http.StatusConflict:
		return res, ErrConflict
	case http.StatusNotFound:
		return res, ErrNotFound
	}
	return res, fmt.Errorf("%s %s: %s", method, u, resp.Status)
}

// Put, CAS, Delete and Get call the server.
func (c *Client) Put(ctx context.Context, key, value string) (Result, error) {
	return c.do(ctx, http.MethodPut, key, "", value)
}
func (c *Client) CAS(ctx context.Context, key string, expected int64, value string) (Result, error) {
	return c.do(ctx, http.MethodPost, key, "?expect="+strconv.FormatInt(expected, 10), value)
}
func (c *Client) Delete(ctx context.Context, key string) (Result, error) {
	return c.do(ctx, http.MethodDelete, key, "", "")
}
func (c *Client) Get(ctx context.Context, key string) (Result, error) {
	return c.do(ctx, http.MethodGet, key, "", "")
}
