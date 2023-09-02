package supervisord

type DaemonState struct {
	StateCode int64  `xmlrpc:"statecode" json:"statecode"`
	StateName string `xmlrpc:"statename" json:"statename"`
}

type DaemonClient struct {
	*RpcClient
}

func (d DaemonClient) APIVersion() (string, error) {
	var ret string
	return ret, d.rpc.Call("supervisor.getAPIVersion", nil, &ret)
}

func (d DaemonClient) SupervisordVersion() (string, error) {
	var ret string
	return ret, d.rpc.Call("supervisor.getSupervisorVersion", nil, &ret)
}

func (d DaemonClient) State() (*DaemonState, error) {
	var ret DaemonState
	return &ret, d.rpc.Call("supervisor.getState", nil, &ret)
}

func (d DaemonClient) Shutdown() error {
	return d.rpc.Call("supervisor.shutdown", nil, nil)
}

func (d DaemonClient) Restart() error {
	return d.rpc.Call("supervisor.restart", nil, nil)
}

func NewDaemonControl(client *RpcClient) *DaemonClient {
	return &DaemonClient{client}
}
