package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/oslab/sysbox/pkg/driver"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/persistence"
	fc "github.com/oslab/sysbox/pkg/provider/firecracker"
	"github.com/oslab/sysbox/pkg/provider/libvirt"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

func main() {
	statePath := flag.String("state", "", "sysbox state path")
	topology := flag.String("topology", "heterogeneous-matrix", "canonical test topology")
	nodeName := flag.String("node", "firecracker", "sysbox_node name")
	target := flag.String("target", "", "IPv4 address to ping")
	pattern := flag.String("pattern", "", "ICMP payload pattern for independently captured probes")
	absent := flag.String("absent-resource", "", "saved canonical resource JSON to verify exact provider deletion")
	query := flag.String("query", "", "state query: netns, libvirt_bridge, root_veth, or libvirt_vm_dir")
	touchPath := flag.String("touch", "", "create a marker in the selected node")
	checkAbsent := flag.String("check-absent", "", "require a path to be absent in the selected node")
	flag.Parse()
	if *absent != "" {
		raw, err := os.ReadFile(*absent)
		if err != nil {
			fatal(err)
		}
		var resource state.Resource
		if err = json.Unmarshal(raw, &resource); err != nil {
			fatal(err)
		}
		cfg := config.MustLoadServiceConfig("")
		var provider interface {
			driver.NodeState
			HostFenceDeletionAbsent(context.Context, substrate.NodeHandle) error
		}
		switch resource.Driver {
		case "firecracker":
			provider = fc.New(cfg.Providers.Firecracker.Kernel, cfg.Providers.Firecracker.Workdir)
		case "libvirt":
			provider = libvirt.New(cfg.Providers.Libvirt.Workdir)
		case "docker":
			d, e := dockerprovider.New()
			if e != nil {
				fatal(e)
			}
			defer d.Close()
			provider = d
		default:
			fatal(fmt.Errorf("unsupported provider"))
		}
		handle, err := resource.ReconstructHandle(provider)
		if err != nil {
			fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		ctx = substrate.WithOperationIdentity(ctx, substrate.OperationIdentity{ActionID: "matrix-absence", OwnerUID: resource.OwnerUID, TopologyUID: resource.OwnerUID, ResourceUID: resource.ResourceUID, Generation: resource.Generation})
		if err = provider.HostFenceDeletionAbsent(ctx, handle); err != nil {
			fatal(err)
		}
		fmt.Println("exact provider deletion absence verified")
		return
	}
	if *pattern != "" && !regexp.MustCompile("^[a-fA-F0-9]{2,32}$").MatchString(*pattern) {
		fatal(fmt.Errorf("invalid ICMP pattern"))
	}
	if *statePath == "" || (*target == "" && *query == "" && *touchPath == "" && *checkAbsent == "") {
		fmt.Fprintln(os.Stderr, "-state and an operation are required")
		os.Exit(2)
	}
	st, err := loadTopologyState(*statePath, *topology)
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
	cfg := config.MustLoadServiceConfig("")
	provider := fc.New(cfg.Providers.Firecracker.Kernel, cfg.Providers.Firecracker.Workdir)
	handle, err := resource.ReconstructHandle(provider)
	if err != nil {
		fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ctx = substrate.WithOperationIdentity(ctx, substrate.OperationIdentity{ActionID: "matrix-probe", OwnerUID: resource.OwnerUID, TopologyUID: resource.OwnerUID, ResourceUID: resource.ResourceUID, Generation: resource.Generation})
	request := substrate.ExecRequest{Program: "ping", Args: []string{"-c", "3", "-W", "2", *target}, Shell: substrate.ShellNone}
	if *pattern != "" {
		request.Args = append([]string{"-p", *pattern}, request.Args...)
	}
	if *touchPath != "" {
		request = substrate.ExecRequest{Program: "touch", Args: []string{*touchPath}, Shell: substrate.ShellNone}
	}
	if *checkAbsent != "" {
		request = substrate.ExecRequest{Program: "test", Args: []string{"!", "-e", *checkAbsent}, Shell: substrate.ShellNone}
	}
	result, err := provider.ExecInNode(ctx, handle, request)
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

func loadMatrixState(path string) (*state.State, error) {
	return loadTopologyState(path, "heterogeneous-matrix")
}
func loadTopologyState(path, topology string) (*state.State, error) {
	backend, resolved, err := persistence.ResolveLocation("", path, topology)
	if err != nil {
		return nil, err
	}
	repo, err := persistence.OpenConfigured(context.Background(), backend, resolved)
	if err != nil {
		return nil, err
	}
	defer repo.Close()
	return repo.LoadState(context.Background(), topology)
}

func printQuery(st *state.State, query string) {
	if strings.HasPrefix(query, "node_id:") {
		name := strings.TrimPrefix(query, "node_id:")
		resource := st.FindResource(address.Resource("sysbox_node", name))
		if resource == nil {
			fatal(fmt.Errorf("node %s not found", name))
		}
		fmt.Println(resource.ContainerID())
		return
	}
	if strings.HasPrefix(query, "node_link:") {
		name := strings.TrimPrefix(query, "node_link:")
		resource := st.FindResource(address.Resource("sysbox_node", name))
		if resource == nil || len(resource.Attachments) == 0 {
			fatal(fmt.Errorf("node %s attachment not found", name))
		}
		a := resource.Attachments[0]
		fmt.Printf("%s|%s\n", a.MAC, strings.Join(a.IPPrefixes, ","))
		return
	}
	if strings.HasPrefix(query, "image_digest:") {
		name := strings.TrimPrefix(query, "image_digest:")
		resource := st.FindResource(address.Resource("sysbox_image", name))
		if resource == nil {
			fatal(fmt.Errorf("image %s not found", name))
		}
		fmt.Println(resource.Str("sha256"))
		return
	}
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
