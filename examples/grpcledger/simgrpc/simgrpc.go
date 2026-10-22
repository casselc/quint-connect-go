// Package simgrpc runs gRPC services inside a qenv simulation: the server's
// generated handlers (any grpc.ServiceDesc) behind a qenv.Service, and a
// client connection (grpc.ClientConnInterface) whose calls cross the
// simulated network. Generated clients (NewXClient(conn)) and servers
// (RegisterXServer(srv, impl)) are used unchanged, and every fault of the
// network is a qenv choice, so seeds explore them and Hegel shrinks them:
//   - "grpc.<name>.conn": "unavailable", the connection refused before
//     the request is sent (codes.Unavailable: nothing happened, a retry is
//     safe);
//   - "rpc.<name>.request": "drop" (never processed) or "duplicate"
//     (processed twice, as a retransmission can);
//   - "rpc.<name>.response": "lost" after the handler ran;
//   - a crash of the server's process, which kills handlers mid-flight and
//     loses requests while it is down.
//
// A request whose response does not arrive within the connection's Timeout
// of virtual time fails with codes.DeadlineExceeded, outcome unknown: the
// ambiguous failure that makes retries dangerous.
//
// Requests and responses are marshalled with protobuf as on the wire, so a
// handler never shares memory with its caller. Outgoing metadata reaches the
// handler as incoming metadata. What is not simulated: streams (NewStream
// returns codes.Unimplemented), peers, and the transport's own retries.
package simgrpc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/casselc/quint-connect-go/qenv"
)

// Server is a gRPC server in a simulation. It implements
// grpc.ServiceRegistrar.
type Server struct {
	Name string
	// Interceptor, if set, wraps every unary handler, as
	// grpc.UnaryInterceptor does on a real server.
	Interceptor grpc.UnaryServerInterceptor

	sim     *qenv.Sim
	methods map[string]method
	svc     *qenv.Service[call, []byte]
	conn    qenv.FaultKind
}

type method struct {
	impl    any
	handler grpc.MethodHandler
}

type call struct {
	method string
	req    []byte
	md     metadata.MD
}

// NewServer starts a gRPC server in process p of s (nil: a process of its
// own that never crashes).
func NewServer(s *qenv.Sim, name string, p *qenv.Process) *Server {
	srv := &Server{Name: name, sim: s, methods: map[string]method{},
		conn: qenv.DeclareFaults("grpc."+name+".conn", "unavailable")}
	srv.svc = qenv.NewService(s, name, p, srv.handle)
	return srv
}

// RegisterService implements grpc.ServiceRegistrar: the generated
// RegisterXServer(srv, impl) registers its unary methods here.
func (s *Server) RegisterService(sd *grpc.ServiceDesc, impl any) {
	for _, m := range sd.Methods {
		s.methods["/"+sd.ServiceName+"/"+m.MethodName] = method{impl: impl, handler: m.Handler}
	}
}

// Calls is the number of handler invocations (duplicates included).
func (s *Server) Calls() int { return s.svc.Calls }

func (s *Server) handle(ctx context.Context, c call) ([]byte, error) {
	m, ok := s.methods[c.method]
	if !ok {
		return nil, status.Errorf(codes.Unimplemented, "simgrpc: %s has no method %s", s.Name, c.method)
	}
	if c.md != nil {
		ctx = metadata.NewIncomingContext(ctx, c.md)
	}
	dec := func(v any) error { return proto.Unmarshal(c.req, v.(proto.Message)) }
	out, err := m.handler(m.impl, ctx, dec, s.Interceptor)
	if err != nil {
		return nil, err
	}
	return proto.Marshal(out.(proto.Message))
}

// Conn is a client connection to a Server. It implements
// grpc.ClientConnInterface.
type Conn struct {
	srv *Server
	// Timeout is how long a call waits for its response, in virtual time
	// (default 1 s). A context's deadline is wall-clock time and means
	// nothing in a simulation.
	Timeout time.Duration
}

// Dial returns a connection to s.
func (s *Server) Dial() *Conn { return &Conn{srv: s, Timeout: time.Second} }

// Invoke implements grpc.ClientConnInterface.
func (c *Conn) Invoke(ctx context.Context, method string, args, reply any, _ ...grpc.CallOption) error {
	qenv.Yield(ctx)
	if c.srv.sim.FaultOf(c.srv.conn) == "unavailable" {
		return status.Errorf(codes.Unavailable, "simgrpc: connection to %s refused", c.srv.Name)
	}
	req, err := proto.Marshal(args.(proto.Message))
	if err != nil {
		return status.Errorf(codes.Internal, "simgrpc: marshal %s request: %v", method, err)
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	resp, err := c.srv.svc.Call(ctx, call{method: method, req: req, md: md.Copy()}, c.Timeout)
	switch {
	case errors.Is(err, qenv.ErrTimeout):
		return status.Errorf(codes.DeadlineExceeded, "simgrpc: %s: no response within %v (outcome unknown)", method, c.Timeout)
	case errors.Is(err, qenv.ErrOverloaded):
		return status.Errorf(codes.ResourceExhausted, "simgrpc: %s overloaded", c.srv.Name)
	case err != nil:
		return err // the handler's status
	}
	return proto.Unmarshal(resp, reply.(proto.Message))
}

// NewStream implements grpc.ClientConnInterface. Streams are not
// simulated.
func (c *Conn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, status.Error(codes.Unimplemented, fmt.Sprintf("simgrpc: streams to %s are not simulated", c.srv.Name))
}
