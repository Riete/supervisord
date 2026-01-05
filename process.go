package supervisord

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-ini/ini"
	"github.com/riete/convert/str"
)

const (
	Stopped               = "STOPPED"
	Starting              = "STARTING"
	Running               = "RUNNING"
	Backoff               = "BACKOFF"
	Stopping              = "STOPPING"
	Exited                = "EXITED"
	Fatal                 = "FATAL"
	Unknown               = "UNKNOWN"
	defaultReadLogBufSize = 8 * 1024
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

func (p *Process) Start(name string) error {
	status, err := p.Status(name)
	if err != nil {
		return err
	}
	if status != Running && status != Starting {
		return p.rpc.Call("supervisor.startProcess", name, nil)
	}
	return nil
}

func (p *Process) StartAll() (StartStopAllRet, bool, error) {
	var ret StartStopAllRet
	if err := p.rpc.Call("supervisor.startAllProcesses", nil, &ret); err != nil {
		return ret, false, err
	}
	return ret, ret.IsAllSuccess(), nil
}

func (p *Process) Stop(name string) error {
	status, err := p.Status(name)
	if err != nil {
		return err
	}
	if status == Running || status == Starting {
		return p.rpc.Call("supervisor.stopProcess", name, nil)
	}
	return nil
}

func (p *Process) StopAll() (StartStopAllRet, bool, error) {
	var ret StartStopAllRet
	if err := p.rpc.Call("supervisor.stopAllProcesses", nil, &ret); err != nil {
		return ret, false, err
	}
	return ret, ret.IsAllSuccess(), nil
}

func (p *Process) Restart(name string) error {
	if err := p.Stop(name); err != nil {
		return err
	}
	return p.Start(name)
}

func (p *Process) Status(name string) (string, error) {
	if ret, err := p.Info(name); err != nil {
		return "", err
	} else {
		return ret.StateName, nil
	}
}

func (p *Process) Info(name string) (*ProcessInfo, error) {
	var ret ProcessInfo
	return &ret, p.rpc.Call("supervisor.getProcessInfo", name, &ret)
}

func (p *Process) AllInfo() ([]ProcessInfo, error) {
	var ret []ProcessInfo
	return ret, p.rpc.Call("supervisor.getAllProcessInfo", nil, &ret)
}

// Reread return [added] [changed] [removed]
func (p *Process) Reread() ([]string, []string, []string, error) {
	var ret [][][]string
	if err := p.rpc.Call("supervisor.reloadConfig", nil, &ret); err != nil {
		return nil, nil, nil, err
	}
	return ret[0][0], ret[0][1], ret[0][2], nil
}

func (p *Process) Add(name string) error {
	return p.rpc.Call("supervisor.addProcessGroup", name, nil)
}

func (p *Process) Remove(name string) error {
	if err := p.Stop(name); err != nil {
		return err
	}
	return p.rpc.Call("supervisor.removeProcessGroup", name, nil)
}

func (p *Process) Update() (map[string][]string, error) {
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

func (p *Process) Options(name, configFile string) (map[string]string, error) {
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

func (p *Process) tailLog(ctx context.Context, name, method string, readBufSize, tailLine int) io.ReadCloser {
	if readBufSize == 0 {
		readBufSize = defaultReadLogBufSize
	}
	r, w := io.Pipe()
	go func() {
		t := time.NewTicker(time.Second)
		var offset int64
		var err error
		defer func() {
			t.Stop()
			if err == nil {
				if ctx.Err() != nil {
					err = ctx.Err()
				} else {
					err = errors.New("tail log quit")
				}
			}
			_ = w.CloseWithError(err)
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				var ret []any
				if err = p.rpc.Call(method, []any{name, offset, readBufSize}, &ret); err != nil {
					return
				}
				data, ok := ret[0].(string)
				if !ok {
					continue
				}
				// first read
				if offset == 0 {
					offset = ret[1].(int64)
					lineCount := strings.Count(data, "\n")
					if lineCount > tailLine {
						data = strings.SplitAfterN(data, "\n", lineCount-tailLine+1)[lineCount-tailLine]
					}
				} else {
					newOffset := ret[1].(int64)
					if newOffset-offset > 0 {
						dataOffset := len(data) - int(newOffset-offset)
						offset = newOffset
						data = data[dataOffset:]
					}
				}
				if len(data) > 0 {
					if _, err = w.Write(str.ToBytes(data)); err != nil {
						return
					}
				}
			}
		}
	}()
	return r
}

func (p *Process) logChan(ctx context.Context, name, method string, readBufSize, tailLine int) chan string {
	ch := make(chan string)
	go func() {
		defer close(ch)
		r := p.tailLog(ctx, name, method, readBufSize, tailLine)
		br := bufio.NewReader(r)
		for {
			line, err := br.ReadString('\n')
			if err == io.EOF {
				time.Sleep(time.Second)
				continue
			}
			if err != nil {
				ch <- err.Error()
				return
			}
			ch <- line
		}
	}()
	return ch
}

func (p *Process) TailStdoutLog(ctx context.Context, name string, readBufSize, tailLine int) chan string {
	return p.logChan(ctx, name, "supervisor.tailProcessStdoutLog", readBufSize, tailLine)
}

func (p *Process) TailStderrLog(ctx context.Context, name string, readBufSize, tailLine int) chan string {
	return p.logChan(ctx, name, "supervisor.tailProcessStderrLog", readBufSize, tailLine)
}

func NewProcessControl(client *RpcClient) *Process {
	return &Process{client}
}
