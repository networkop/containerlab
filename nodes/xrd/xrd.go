// Copyright 2022 Nokia
// Licensed under the BSD 3-Clause License.
// SPDX-License-Identifier: BSD-3-Clause

package xrd

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/srl-labs/containerlab/netconf"
	"github.com/srl-labs/containerlab/nodes"
	"github.com/srl-labs/containerlab/types"
	"github.com/srl-labs/containerlab/utils"
)

var (
	kindnames          = []string{"xrd", "cisco_xrd"}
	defaultCredentials = nodes.NewCredentials("clab", "clab@123")
	xrdEnv             = map[string]string{
		"XR_FIRST_BOOT_CONFIG": "/etc/xrd/first-boot.cfg",
		"XR_MGMT_INTERFACES":   "linux:eth0,xr_name=Mg0/RP0/CPU0/0,chksum,snoop_v4,snoop_v6",
	}

	//go:embed xrd.cfg
	cfgTemplate string
)

const (
	scrapliPlatformName = "cisco_iosxr"
)

// Register registers the node in the NodeRegistry.
func Register(r *nodes.NodeRegistry) {
	r.Register(kindnames, func() nodes.Node {
		return new(xrd)
	}, defaultCredentials)
}

type xrd struct {
	nodes.DefaultNode
}

func (n *xrd) Init(cfg *types.NodeConfig, opts ...nodes.NodeOption) error {
	// Init DefaultNode
	n.DefaultNode = *nodes.NewDefaultNode(n)

	n.Cfg = cfg
	for _, o := range opts {
		o(n)
	}

	n.Cfg.Binds = append(n.Cfg.Binds,
		// mount first-boot config file
		fmt.Sprint(filepath.Join(n.Cfg.LabDir, "first-boot.cfg"), ":/etc/xrd/first-boot.cfg"),
		// persist data by mounting /xr-storage
		fmt.Sprint(filepath.Join(n.Cfg.LabDir, "xr-storage"), ":/xr-storage"),
	)

	return nil
}

func (n *xrd) PreDeploy(ctx context.Context, _, _, _ string) error {
	n.genInterfacesEnv()

	utils.CreateDirectory(n.Cfg.LabDir, 0777)

	return n.createXRDFiles(ctx)
}

func (n *xrd) SaveConfig(_ context.Context) error {
	err := netconf.SaveConfig(n.Cfg.LongName,
		defaultCredentials.GetUsername(),
		defaultCredentials.GetPassword(),
		scrapliPlatformName,
	)
	if err != nil {
		return err
	}

	log.Infof("saved %s running configuration to startup configuration file\n", n.Cfg.ShortName)
	return nil
}

func (n *xrd) createXRDFiles(_ context.Context) error {
	nodeCfg := n.Config()
	// generate xr-storage directory
	utils.CreateDirectory(filepath.Join(n.Cfg.LabDir, "xr-storage"), 0777)
	// generate first-boot config
	cfg := filepath.Join(n.Cfg.LabDir, "first-boot.cfg")
	nodeCfg.ResStartupConfig = cfg

	// set mgmt IPv4/IPv6 gateway as it is already known by now
	// since the container network has been created before we launch nodes
	// and mgmt gateway can be used in xrd.Cfg template to configure default route for mgmt
	nodeCfg.MgmtIPv4Gateway = n.Runtime.Mgmt().IPv4Gw
	nodeCfg.MgmtIPv6Gateway = n.Runtime.Mgmt().IPv6Gw

	// use startup config file provided by a user
	if nodeCfg.StartupConfig != "" {
		c, err := os.ReadFile(nodeCfg.StartupConfig)
		if err != nil {
			return err
		}
		cfgTemplate = string(c)
	}

	err := n.GenerateConfig(nodeCfg.ResStartupConfig, cfgTemplate)
	if err != nil {
		return err
	}

	return err
}

// genInterfacesEnv populates the content of a required env var that sets the interface mapping rules.
func (n *xrd) genInterfacesEnv() {
	// xrd-control-plane variant needs XR_INTERFACE ENV var to be populated for all active interface
	// here we take the number of links users set in the topology to get the right # of links
	var interfaceEnvVar string

	for _, ep := range n.Config().Endpoints {
		// ifName is a linux interface name with dashes swapped for slashes to be used in the config
		ifName := strings.ReplaceAll(ep.EndpointName, "-", "/")
		interfaceEnvVar += fmt.Sprintf("linux:%s,xr_name=%s;", ep.EndpointName, ifName)
	}

	interfaceEnv := map[string]string{"XR_INTERFACES": interfaceEnvVar}

	n.Cfg.Env = utils.MergeStringMaps(xrdEnv, interfaceEnv, n.Cfg.Env)
}

// CheckInterfaceName checks if a name of the interface referenced in the topology file correct.
func (n *xrd) CheckInterfaceName() error {
	ifRe := regexp.MustCompile(`^Gi0-0-0-\d+$`)
	for _, e := range n.Config().Endpoints {
		if !ifRe.MatchString(e.EndpointName) {
			return fmt.Errorf("cisco XRd interface name %q doesn't match the required pattern. XRd interfaces should be named as Gi0-0-0-X where X is the interface number", e.EndpointName)
		}
	}

	return nil
}
