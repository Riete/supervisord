package supervisord

import (
	"github.com/go-ini/ini"
)

type InetHttpServer struct {
	ServerUrl string `ini:"port"`
	Username  string `ini:"username"`
	Password  string `ini:"password"`
}

type UnixSock struct {
	SockPath string `ini:"serverurl"`
}

type RpcConfig struct {
	InetHttpServer InetHttpServer `ini:"inet_http_server"`
	UnixSock       UnixSock       `ini:"supervisorctl"`
}

func ParseRpcConfig(configFile string) (*RpcConfig, error) {
	var cfg RpcConfig
	return &cfg, ini.MapTo(&cfg, configFile)
}
