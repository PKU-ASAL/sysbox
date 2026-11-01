package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/oslab/sysbox/pkg/buildinfo"
	"github.com/oslab/sysbox/pkg/taskprotocol"
	"github.com/oslab/sysbox/pkg/windowstask"
	"io"
	"os"
)

func main() {
	version := flag.Bool("version", false, "print build identity")
	worker := flag.Bool("worker", false, "internal detached task supervisor")
	guard := flag.Bool("guard", false, "internal independent job guard")
	rpc := flag.Bool("guest-task", false, "serve one task protocol request")
	config := flag.String("config", windowstask.DefaultConfigPath, "protected helper configuration")
	id := flag.String("id", "", "guard task ID")
	flag.Parse()
	if *version {
		fmt.Printf("version=%s commit=%s built=%s\n", buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime)
		return
	}
	engine, err := windowstask.LoadWindowsEngine(*config)
	if err == nil {
		switch {
		case *worker:
			var request taskprotocol.Request
			err = json.NewDecoder(io.LimitReader(os.Stdin, 32<<20)).Decode(&request)
			if err == nil {
				err = engine.RunWorker(context.Background(), request, os.Stdout)
			}
		case *guard:
			err = engine.RunGuard(context.Background(), *id, os.Stdout)
		case *rpc:
			var call windowstask.Call
			call, err = windowstask.ReadCall(os.Stdin)
			if err == nil {
				err = json.NewEncoder(os.Stdout).Encode(engine.Handle(context.Background(), call))
			}
		default:
			err = fmt.Errorf("specify --guest-task")
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
