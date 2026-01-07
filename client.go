package supervisord

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/kolo/xmlrpc"
)

const DefaultSupervisordConfigFile = "/etc/supervisord.conf"

type basicAuthTransport struct {
	username string
	password string
	rt       http.RoundTripper
}

func (b basicAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.SetBasicAuth(b.username, b.password)
	return b.rt.RoundTrip(req)
}

type RpcClient struct {
	rpc        *xmlrpc.Client
	configFile string
	httpServer struct {
		url      string
		username string
		password string
	}
	unixSock struct {
		path string
	}
}

type Option func(*RpcClient)

func WithDefaultConfigFile() Option {
	return func(c *RpcClient) {
		c.configFile = DefaultSupervisordConfigFile
	}
}

func WithConfigFile(path string) Option {
	return func(c *RpcClient) {
		c.configFile = path
	}
}

func WithUnixSock(path string) Option {
	return func(c *RpcClient) {
		c.unixSock.path = path
	}
}

func WithHttpServer(url, username, password string) Option {
	return func(c *RpcClient) {
		c.httpServer.url = strings.TrimPrefix(url, "http://")
		c.httpServer.username = username
		c.httpServer.password = password
	}
}

func (r *RpcClient) initHttpRpcClient() error {
	var err error
	tr := http.DefaultTransport
	if r.httpServer.username != "" && r.httpServer.password != "" {
		tr = &basicAuthTransport{username: r.httpServer.username, password: r.httpServer.password, rt: tr}
	}
	r.rpc, err = xmlrpc.NewClient(fmt.Sprintf("http://%s/RPC2", r.httpServer.url), tr)
	return err
}

func (r *RpcClient) initUnixSockRpcClient() error {
	dialer := func(ctx context.Context, _, _ string) (net.Conn, error) {
		d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		return d.DialContext(ctx, "unix", r.unixSock.path)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = dialer
	var err error
	// ignore this rpc address, only for url.Parse() and /RPC2 context
	r.rpc, err = xmlrpc.NewClient("http://127.0.0.1/RPC2", tr)
	return err
}

func (r *RpcClient) initRpcClient() error {
	if r.configFile != "" {
		cfg, err := ParseRpcConfig(r.configFile)
		if err != nil {
			return err
		}
		r.httpServer.url = cfg.InetHttpServer.ServerUrl
		r.httpServer.username = cfg.InetHttpServer.Username
		r.httpServer.password = cfg.InetHttpServer.Username
		r.unixSock.path = strings.TrimPrefix(cfg.UnixSock.SockPath, "unix://")
	}
	if r.unixSock.path != "" {
		if err := r.initUnixSockRpcClient(); err == nil {
			return nil
		}
	}
	if r.httpServer.url != "" {
		if err := r.initHttpRpcClient(); err == nil {
			return nil
		}
	}
	return errors.New("init rpc client error: inet_http_server is disabled or find unix sock path failed")
}

func (r *RpcClient) Close() error {
	return r.rpc.Close()
}

func NewRpcClient(option Option) (*RpcClient, error) {
	r := &RpcClient{}
	option(r)
	return r, r.initRpcClient()
}
