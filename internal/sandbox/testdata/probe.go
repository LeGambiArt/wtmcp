// probe is a test binary used by sandbox integration tests.
// It reads a JSON command from stdin and writes a JSON result to stdout.
//
//	{"cmd": "read_file", "path": "/etc/shadow"}
//	{"cmd": "write_file", "path": "/tmp/test", "data": "hello"}
//	{"cmd": "dial_tcp", "addr": "1.1.1.1:443"}
//	{"cmd": "echo", "msg": "hello"}
//	{"cmd": "env", "key": "GITLAB_TOKEN"}
//	{"cmd": "tmpdir"}
//	{"cmd": "stat", "path": "/some/dir"}
//	{"cmd": "eval_symlinks", "path": "/some/dir"}
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

type request struct {
	Cmd  string `json:"cmd"`
	Path string `json:"path,omitempty"`
	Data string `json:"data,omitempty"`
	Addr string `json:"addr,omitempty"`
	Msg  string `json:"msg,omitempty"`
	Key  string `json:"key,omitempty"`
}

type response struct {
	OK    bool   `json:"ok"`
	Data  string `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

func main() {
	var req request
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		writeResponse(response{Error: fmt.Sprintf("decode: %v", err)})
		return
	}

	switch req.Cmd {
	case "read_file":
		data, err := os.ReadFile(req.Path) //nolint:gosec
		if err != nil {
			writeResponse(response{Error: err.Error()})
			return
		}
		writeResponse(response{OK: true, Data: string(data)})

	case "write_file":
		err := os.WriteFile(req.Path, []byte(req.Data), 0o644) //nolint:gosec
		if err != nil {
			writeResponse(response{Error: err.Error()})
			return
		}
		writeResponse(response{OK: true})

	case "dial_tcp":
		conn, err := net.DialTimeout("tcp", req.Addr, 2*time.Second)
		if err != nil {
			writeResponse(response{Error: err.Error()})
			return
		}
		conn.Close()
		writeResponse(response{OK: true})

	case "echo":
		writeResponse(response{OK: true, Data: req.Msg})

	case "env":
		writeResponse(response{OK: true, Data: os.Getenv(req.Key)})

	case "tmpdir":
		writeResponse(response{OK: true, Data: os.TempDir()})

	case "stat":
		info, err := os.Stat(req.Path)
		if err != nil {
			writeResponse(response{Error: err.Error()})
			return
		}
		writeResponse(response{OK: true, Data: fmt.Sprintf("mode=%s size=%d", info.Mode(), info.Size())})

	case "eval_symlinks":
		resolved, err := filepath.EvalSymlinks(req.Path)
		if err != nil {
			writeResponse(response{Error: err.Error()})
			return
		}
		writeResponse(response{OK: true, Data: resolved})

	default:
		writeResponse(response{Error: fmt.Sprintf("unknown cmd: %s", req.Cmd)})
	}
}

func writeResponse(r response) {
	json.NewEncoder(os.Stdout).Encode(r) //nolint:errcheck
}
