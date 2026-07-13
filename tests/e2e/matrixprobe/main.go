package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	fc "github.com/oslab/sysbox/pkg/provider/firecracker"
	"github.com/oslab/sysbox/pkg/provider/libvirt"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

func main() {
	statePath := flag.String("state", "", "sysbox state path")
	nodeName := flag.String("node", "firecracker", "sysbox_node name")
	target := flag.String("target", "", "IPv4 address to ping")
	query := flag.String("query", "", "state query: netns, libvirt_bridge, root_veth, or libvirt_vm_dir")
	flag.Parse()
	if *statePath == "" || (*target == "" && *query == "") {
		fmt.Fprintln(os.Stderr, "-state and either -target or -query are required")
		os.Exit(2)
	}
	st, err := state.NewManager(*statePath).Load()
	if err != nil {
		fatal(err)
	}
	if *query != "" {
		printQuery(st, *query)
		return
	}
	resource := st.FindResource(address.Resource("sysbox_node", *nodeName))
	if resource == nil {
		fatal(fmt.Errorf("node %s not found", *nodeName))
	}
	provider := fc.New("", "")
	handle, err := resource.ReconstructHandle(provider)
	if err != nil {
		fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := provider.ExecInNode(ctx, handle, substrate.ExecSpec{Cmd: []string{"ping", "-c", "3", "-W", "2", *target}})
	if err != nil {
		fatal(err)
	}
	if result.Stdout != "" {
		fmt.Print(result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprint(os.Stderr, result.Stderr)
	}
	if result.ExitCode != 0 {
		fatal(fmt.Errorf("ping %s exited %d", *target, result.ExitCode))
	}
}

func printQuery(st *state.State, query string) {
	switch query {
	case "netns", "libvirt_bridge", "root_veth":
		resource := st.FindResource(address.Resource("sysbox_network", "matrix"))
		if resource == nil {
			fatal(fmt.Errorf("network matrix not found"))
		}
		fmt.Println(resource.Str(query))
	case "libvirt_vm_dir":
		resource := st.FindResource(address.Resource("sysbox_node", "libvirt"))
		if resource == nil {
			fatal(fmt.Errorf("node libvirt not found"))
		}
		provider := libvirt.New()
		handle, err := resource.ReconstructHandle(provider)
		if err != nil {
			fatal(err)
		}
		fmt.Println(handle.Provider.(*libvirt.HandleState).VMDir)
	default:
		fatal(fmt.Errorf("unsupported query %q", query))
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
