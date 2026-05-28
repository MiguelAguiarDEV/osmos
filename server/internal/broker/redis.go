package broker

import (
	"context"
	"encoding/json"

	"github.com/redis/go-redis/v9"
)

// Redis fans clips out across instances via a single pub/sub channel. Each
// instance subscribes and delivers received clips to its local connections, so
// a user's devices can connect to any instance behind a load balancer.
type Redis struct {
	client  *redis.Client
	channel string
	h       Handler
	ctx     context.Context
	cancel  context.CancelFunc
}

type redisMsg struct {
	User    string `json:"u"`
	From    string `json:"f"`
	Payload []byte `json:"p"`
}

// NewRedis connects to the given redis URL (e.g. redis://host:6379/0).
func NewRedis(url, channel string) (*Redis, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	if channel == "" {
		channel = "clipsync"
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Redis{client: redis.NewClient(opt), channel: channel, ctx: ctx, cancel: cancel}
	if err := r.client.Ping(ctx).Err(); err != nil {
		cancel()
		_ = r.client.Close()
		return nil, err
	}
	return r, nil
}

func (r *Redis) SetHandler(h Handler) {
	r.h = h
	sub := r.client.Subscribe(r.ctx, r.channel)
	go func() {
		defer sub.Close()
		ch := sub.Channel()
		for {
			select {
			case <-r.ctx.Done():
				return
			case m, ok := <-ch:
				if !ok {
					return
				}
				var rm redisMsg
				if err := json.Unmarshal([]byte(m.Payload), &rm); err != nil {
					continue
				}
				if r.h != nil {
					// Each instance delivers to its own connections only; no
					// retry (the device may legitimately be on another instance).
					r.h(rm.User, rm.From, rm.Payload)
				}
			}
		}
	}()
}

func (r *Redis) Publish(userID, fromDevice string, payload []byte) {
	b, err := json.Marshal(redisMsg{User: userID, From: fromDevice, Payload: payload})
	if err != nil {
		return
	}
	_ = r.client.Publish(r.ctx, r.channel, b).Err()
}

func (r *Redis) Close() error {
	r.cancel()
	return r.client.Close()
}
