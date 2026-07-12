package api

import (
	"github.com/oslab/sysbox/pkg/driver"
	networkprovider "github.com/oslab/sysbox/pkg/provider/network"
)

func init() {
	_ = driver.DefaultRegistry.Register(driver.Descriptor{
		Name:         "network",
		Version:      "test",
		LinuxNetwork: networkprovider.Driver{},
	})
}
