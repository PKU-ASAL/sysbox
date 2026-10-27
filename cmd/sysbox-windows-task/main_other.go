//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() { fmt.Fprintln(os.Stderr, "sysbox-windows-task must be built for Windows"); os.Exit(1) }
