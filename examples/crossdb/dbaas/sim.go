package dbaas

import (
	"context"
	"errors"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
)

type simReq struct {
	Op       string
	In       CreateInput
	ID       string
	Token    string
	Password string
}

type simResp struct {
	DB  Database
	Err error
}

// SimClient is the API across a simulated network: requests and responses
// can be lost or duplicated (qenv.Service), and the cloud throttles
// ("fault.dbaas.throttle").
type SimClient struct {
	svc     *qenv.Service[simReq, simResp]
	Timeout time.Duration
}

// NewSim serves cloud in s, on the virtual clock, and returns its client.
func NewSim(s *qenv.Sim, cloud *Cloud) *SimClient {
	cloud.Now = s.Now
	svc := qenv.NewService(s, "dbaas", nil, func(ctx context.Context, r simReq) (simResp, error) {
		if s.Fault("dbaas.throttle", 2) == 1 {
			return simResp{Err: ErrThrottled}, nil
		}
		var out simResp
		switch r.Op {
		case "create":
			out.DB, out.Err = cloud.Create(ctx, r.In)
		case "get":
			out.DB, out.Err = cloud.Get(ctx, r.ID)
		case "find":
			out.DB, out.Err = cloud.FindByToken(ctx, r.Token)
		case "password":
			out.Err = cloud.SetPassword(ctx, r.ID, r.Password)
		case "delete":
			out.Err = cloud.Delete(ctx, r.ID)
		}
		return out, nil
	})
	return &SimClient{svc: svc, Timeout: 2 * time.Second}
}

func (c *SimClient) call(ctx context.Context, r simReq) (Database, error) {
	out, err := c.svc.Call(ctx, r, c.Timeout)
	if errors.Is(err, qenv.ErrTimeout) {
		return Database{}, errors.New("dbaas: request timed out (outcome unknown)")
	}
	if err != nil {
		return Database{}, err
	}
	return out.DB, out.Err
}

// Create implements API.
func (c *SimClient) Create(ctx context.Context, in CreateInput) (Database, error) {
	return c.call(ctx, simReq{Op: "create", In: in})
}

// Get implements API.
func (c *SimClient) Get(ctx context.Context, id string) (Database, error) {
	return c.call(ctx, simReq{Op: "get", ID: id})
}

// FindByToken implements API.
func (c *SimClient) FindByToken(ctx context.Context, token string) (Database, error) {
	return c.call(ctx, simReq{Op: "find", Token: token})
}

// SetPassword implements API.
func (c *SimClient) SetPassword(ctx context.Context, id, password string) error {
	_, err := c.call(ctx, simReq{Op: "password", ID: id, Password: password})
	return err
}

// Delete implements API.
func (c *SimClient) Delete(ctx context.Context, id string) error {
	_, err := c.call(ctx, simReq{Op: "delete", ID: id})
	return err
}
