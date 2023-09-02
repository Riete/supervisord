package supervisord

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/go-ini/ini"
)

const (
	Stopped  = "STOPPED"
	Starting = "STARTING"
	Running  = "RUNNING"
	Backoff  = "BACKOFF"
	Stopping = "STOPPING"
	Exited   = "EXITED"
	Fatal    = "FATAL"
	Unknown  = "UNKNOWN"
)

type ProcessInfo struct {
	Name          string `xmlrpc:"name" json:"name"`
	Group         string `xmlrpc:"group" json:"group"`
	Description   string `xmlrpc:"description" json:"description"`
	Start         int64  `xmlrpc:"start" json:"start"`
	Stop          int64  `xmlrpc:"stop" json:"stop"`
	Now           int64  `xmlrpc:"now" json:"now"`
	State         int64  `xmlrpc:"state" json:"state"`
	StateName     string `xmlrpc:"statename" json:"statename"`
	SpawnErr      string `xmlrpc:"spawnerr" json:"spawnerr"`
	ExitStatus    int64  `xmlrpc:"exitstatus" json:"exitstatus"`
	LogFile       string `xmlrpc:"logfile" json:"logfile"`
	StdoutLogFile string `xmlrpc:"stdout_logfile" json:"stdout_logfile"`
	StderrLogFile string `xmlrpc:"stderr_logfile" json:"stderr_logfile"`
	Pid           int64  `xmlrpc:"pid" json:"pid"`
}

type StartStopRet struct {
	Description string `xmlrpc:"description" json:"description"`
	Name        string `xmlrpc:"name" json:"name"`
	Group       string `xmlrpc:"group" json:"group"`
	Status      int64  `xmlrpc:"status" json:"status"`
}

type StartStopAllRet []StartStopRet

func (s StartStopAllRet) IsAllSuccess() bool {
	for _, r := range s {
		if r.Status != 80 {
			return false
		}
	}
	return true
}

type Process struct {
	*RpcClient
}

func (p Process) Start(name string) error {
	status, err := p.Status(name)
	if err != nil {
		return err
	}
	if status != Running && status != Starting {
		return p.rpc.Call("supervisor.startProcess", name, nil)
	}
	return nil
}

func (p Process) StartAll() (StartStopAllRet, bool, error) {
	var ret StartStopAllRet
	if err := p.rpc.Call("supervisor.startAllProcesses", nil, &ret); err != nil {
		return ret, false, err
	}
	return ret, ret.IsAllSuccess(), nil
}

func (p Process) Stop(name string) error {
	status, err := p.Status(name)
	if err != nil {
		return err
	}
	if status == Running || status == Starting {
		return p.rpc.Call("supervisor.stopProcess", name, nil)
	}
	return nil
}

func (p Process) StopAll() (StartStopAllRet, bool, error) {
	var ret StartStopAllRet
	if err := p.rpc.Call("supervisor.stopAllProcesses", nil, &ret); err != nil {
		return ret, false, err
	}
	return ret, ret.IsAllSuccess(), nil
}

func (p Process) Restart(name string) error {
	if err := p.Stop(name); err != nil {
		return err
	}
	return p.Start(name)
}

func (p Process) Status(name string) (string, error) {
	if ret, err := p.Info(name); err != nil {
		return "", err
	} else {
		return ret.StateName, nil
	}
}

func (p Process) Info(name string) (*ProcessInfo, error) {
	var ret ProcessInfo
	return &ret, p.rpc.Call("supervisor.getProcessInfo", name, &ret)
}

func (p Process) AllInfo() ([]ProcessInfo, error) {
	var ret []ProcessInfo
	return ret, p.rpc.Call("supervisor.getAllProcessInfo", nil, &ret)
}

// Reread return [added] [changed] [removed]
func (p Process) Reread() ([]string, []string, []string, error) {
	var ret [][][]string
	if err := p.rpc.Call("supervisor.reloadConfig", nil, &ret); err != nil {
		return nil, nil, nil, err
	}
	return ret[0][0], ret[0][1], ret[0][2], nil
}

func (p Process) Add(name string) error {
	return p.rpc.Call("supervisor.addProcessGroup", name, nil)
}

func (p Process) Remove(name string) error {
	if err := p.Stop(name); err != nil {
		return err
	}
	return p.rpc.Call("supervisor.removeProcessGroup", name, nil)
}

func (p Process) Update() (map[string][]string, error) {
	m := make(map[string][]string)
	added, changed, removed, err := p.Reread()
	if err != nil {
		return m, err
	}
	m["added"] = added
	m["changed"] = changed
	m["removed"] = removed
	removed = append(removed, changed...)
	added = append(added, changed...)
	for _, name := range removed {
		if err := p.Remove(name); err != nil {
			return m, err
		}
	}
	for _, name := range added {
		if err := p.Add(name); err != nil {
			return m, err
		}
	}
	return m, nil
}

func (p Process) Options(name, configFile string) (map[string]string, error) {
	cfg, err := ini.LoadSources(ini.LoadOptions{AllowPythonMultilineValues: true}, configFile)
	if err != nil {
		return nil, err
	}
	s := cfg.Section(fmt.Sprintf("program:%s", name))
	m := make(map[string]string)
	for _, k := range s.KeyStrings() {
		m[k] = s.Key(k).Value()
	}
	return m, nil
}

func (p Process) logTail(ctx context.Context, r io.ReadCloser, ch chan<- string) {
	defer func() {
		if err := recover(); err != nil {
			ch <- fmt.Sprintf("%v", err)
		}
		close(ch)
		r.Close()
	}()
	rb := bufio.NewReader(r)
	for {
		select {
		case <-ctx.Done():
			return
		default:
			d, err := rb.ReadString('\n')
			if err == io.EOF {
				time.Sleep(time.Second)
				continue
			}
			if err != nil {
				ch <- err.Error()
				return
			}
			ch <- d
		}
	}
}

func (p Process) openLogFile(path string, offset int64) (io.ReadCloser, error) {
	r, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if offset < 0 {
		offset = -offset
	}
	s, _ := os.Stat(path)
	if s.Size() < offset {
		offset = s.Size()
	}
	if _, err := r.Seek(-offset, io.SeekEnd); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

func (p Process) StdoutLog(ctx context.Context, name string, offset int64) (<-chan string, error) {
	info, err := p.Info(name)
	if err != nil {
		return nil, err
	}
	r, err := p.openLogFile(info.StdoutLogFile, offset)
	if err != nil {
		return nil, err
	}
	ch := make(chan string)
	go p.logTail(ctx, r, ch)
	return ch, nil
}

func (p Process) StderrLog(ctx context.Context, name string, offset int64) (<-chan string, error) {
	info, err := p.Info(name)
	if err != nil {
		return nil, err
	}
	path := info.StderrLogFile
	if path == "" {
		path = info.StdoutLogFile
	}
	r, err := p.openLogFile(path, offset)
	if err != nil {
		return nil, err
	}
	ch := make(chan string)
	go p.logTail(ctx, r, ch)
	return ch, nil
}

func NewProcessControl(client *RpcClient) *Process {
	return &Process{client}
}
