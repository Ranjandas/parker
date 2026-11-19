package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/tproxy"
)

// sidecarCmd runs Envoy for a Connect service: `parker sidecar [flags] --
// consul connect envoy ARGS -- ENVOY_ARGS`. It takes the bootstrap from
// consul, marks Envoy's upstream sockets, applies the transparent proxy
// rules when asked and then becomes Envoy.
func sidecarCmd() *cobra.Command {
	var r tproxy.Redirect
	var transparent bool
	c := &cobra.Command{
		Use:    expand.SidecarCmd + " [flags] -- consul connect envoy ARGS [-- ENVOY_ARGS]",
		Short:  "Run a Connect sidecar's Envoy (run by parker up)",
		Hidden: true,
		Args:   cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			consulArgs, envoyArgs := tproxy.SplitArgs(args)
			var out, stderr bytes.Buffer
			boot := exec.Command(consulArgs[0], append(consulArgs[1:], "-bootstrap")...)
			boot.Stdout, boot.Stderr = &out, &stderr
			if err := boot.Run(); err != nil {
				return fmt.Errorf("%s -bootstrap: %w: %s", strings.Join(consulArgs, " "), err, strings.TrimSpace(stderr.String()))
			}
			b, err := tproxy.PatchBootstrap(out.Bytes())
			if err != nil {
				return err
			}
			dir, _ := os.UserHomeDir() // the node's dir under parker
			if dir == "" {
				dir = os.TempDir()
			}
			path := filepath.Join(dir, "envoy-"+sidecarName(consulArgs)+".json")
			if err := os.WriteFile(path, b, 0o600); err != nil {
				return err
			}
			if transparent {
				ipt, err := iptablesPath()
				if err != nil {
					return err
				}
				r.ExcludeIn = append(r.ExcludeIn, tproxy.AgentInboundPorts...)
				r.ExcludeOut = append(r.ExcludeOut, tproxy.AgentOutboundPorts...)
				if err := tproxy.Apply(r, func(a ...string) error {
					cmd := exec.Command(ipt, append([]string{"-w"}, a...)...)
					if o, err := cmd.CombinedOutput(); err != nil {
						return fmt.Errorf("iptables %s: %w: %s", strings.Join(a, " "), err, strings.TrimSpace(string(o)))
					}
					return nil
				}); err != nil {
					return err
				}
				fmt.Printf("transparent proxy: inbound -> %d, outbound -> %d, dns -> %d\n", r.InboundPort, r.OutboundPort, r.DNSPort)
			}
			envoy, err := exec.LookPath("envoy")
			if err != nil {
				return err
			}
			argv := append([]string{"envoy", "--config-path", path, "--disable-hot-restart"}, envoyArgs...)
			return syscall.Exec(envoy, argv, os.Environ())
		},
	}
	c.Flags().BoolVar(&transparent, "transparent", false, "redirect the node's TCP traffic through Envoy")
	c.Flags().IntVar(&r.InboundPort, "inbound-port", 0, "Envoy's public listener port")
	c.Flags().IntVar(&r.OutboundPort, "outbound-port", 15001, "Envoy's outbound listener port")
	c.Flags().IntVar(&r.DNSPort, "dns-port", 8600, "redirect DNS to this local port (0: leave DNS alone)")
	return c
}

// sidecarName is the -sidecar-for service, for the bootstrap file name.
func sidecarName(args []string) string {
	for i, a := range args {
		if a == "-sidecar-for" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return "sidecar"
}

func iptablesPath() (string, error) {
	if p, err := exec.LookPath("iptables"); err == nil {
		return p, nil
	}
	for _, p := range []string{"/usr/sbin/iptables", "/sbin/iptables"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("connect: tproxy needs iptables on the host")
}
