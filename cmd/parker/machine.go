package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ranjandas/parker/internal/daemon"
	"github.com/Ranjandas/parker/internal/machine"
)

// machineTarget points the CLI at a running parker machine when nothing
// else was chosen (macOS: no local daemon is possible).
func machineTarget() (daemon.Target, bool) {
	if runtime.GOOS == "linux" {
		return daemon.Target{}, false
	}
	m, err := machine.Default()
	if err != nil {
		return daemon.Target{}, false
	}
	c, ok := m.Connection()
	if !ok {
		return daemon.Target{}, false
	}
	return daemon.Target{Host: c.Host, Token: c.Token, CAFile: c.CAFile}, true
}

func machineCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "machine",
		Short: "Run labs on macOS: a small Linux VM with the Parker daemon inside",
		Long: "parker machine runs a small Linux VM (Apple's Virtualization framework) with the\n" +
			"Parker daemon in it. While it runs, parker on this Mac uses that daemon, with no\n" +
			"PARKER_HOST to set. The VM sees none of your files unless you --mount them at init.",
		Example: "  parker machine init\n  parker machine start\n  parker up examples/minimal.yaml",
	}
	c.AddCommand(machineInitCmd(), machineStartCmd(), machineStopCmd(), machineRmCmd(), machineSSHCmd())
	return c
}

func machineInitCmd() *cobra.Command {
	o := machine.InitOptions{}
	c := &cobra.Command{
		Use:   "init",
		Short: "Create the machine: download the Linux image and prepare its disk",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := machine.Default()
			if err != nil {
				return err
			}
			for i, d := range o.Mounts {
				if o.Mounts[i], err = filepath.Abs(d); err != nil {
					return err
				}
				if fi, err := os.Stat(o.Mounts[i]); err != nil || !fi.IsDir() {
					return fmt.Errorf("--mount %s: not a directory", d)
				}
			}
			if err := m.Init(o, os.Stderr); err != nil {
				return err
			}
			say("created parker machine in %s", m.Dir)
			say("next: parker machine start")
			return nil
		},
	}
	c.Flags().UintVar(&o.CPUs, "cpus", uint(min(runtime.NumCPU(), 4)), "virtual CPUs")
	c.Flags().Uint64Var(&o.MemoryMiB, "memory", 4096, "memory in MiB")
	c.Flags().Uint64Var(&o.DiskGiB, "disk", 30, "disk size in GiB (grows on demand on the Mac)")
	c.Flags().StringArrayVar(&o.Mounts, "mount", nil, "share a host directory into the VM at the same path (for labs that use files on this Mac); repeatable")
	return c
}

func machineStartCmd() *cobra.Command {
	var foreground bool
	c := &cobra.Command{
		Use:   "start",
		Short: "Boot the machine and point parker on this Mac at its daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !machine.Supported {
				return machine.Unsupported()
			}
			m, err := machine.Default()
			if err != nil {
				return err
			}
			if foreground {
				ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
				defer stop()
				logf := func(f string, a ...any) {
					fmt.Fprintf(os.Stderr, "%s %s\n", time.Now().Format(time.TimeOnly), fmt.Sprintf(f, a...))
				}
				return m.Run(ctx, logf)
			}
			conf, err := m.LoadConfig()
			if err != nil {
				return err
			}
			if pid := m.PID(); pid != 0 {
				if r, ok := m.ReadReady(); ok {
					say("parker machine is already running at %s (pid %d)", r.IP, pid)
					return nil
				}
				return fmt.Errorf("parker machine is already running (pid %d) but not ready; see %s", pid, m.LogPath())
			}
			if err := m.SetDaemon(daemonRelease(), os.Getenv("PARKER_MACHINE_BINARY")); err != nil {
				return err
			}
			_ = os.Remove(m.ReadyPath())
			self, err := os.Executable()
			if err != nil {
				return err
			}
			lf, err := os.OpenFile(m.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				return err
			}
			defer lf.Close()
			vm := exec.Command(self, "machine", "start", "--foreground")
			vm.Stdout, vm.Stderr = lf, lf
			vm.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			began := time.Now()
			if err := vm.Start(); err != nil {
				return err
			}
			exited := make(chan struct{})
			go func() { _ = vm.Wait(); close(exited) }()
			alive := func() bool {
				select {
				case <-exited:
					return false
				default:
					return true
				}
			}
			note := ""
			if _, err := os.Stat(m.EFIVarsPath()); err != nil {
				note = "; the first boot installs the daemon"
			}
			fmt.Fprintf(os.Stderr, "booting parker machine (%s%s)\n", conf.Image, note)
			n := 0
			r, err := m.WaitReady(began, 10*time.Minute, alive, func() {
				n++
				if n%10 == 0 {
					fmt.Fprintf(os.Stderr, "  still booting (%ds)\n", n)
				}
			})
			if err != nil {
				return fmt.Errorf("%w\n%s\n(console: %s)", err, tailFile(m.LogPath(), 8), m.ConsolePath())
			}
			say("parker machine is up at %s in %s (daemon %s)", r.IP, time.Since(began).Round(time.Second), r.Version)
			say("parker on this Mac now uses it: try `parker up examples/minimal.yaml`")
			return nil
		},
	}
	c.Flags().BoolVar(&foreground, "foreground", false, "run the VM in this process (used by start itself)")
	_ = c.Flags().MarkHidden("foreground")
	return c
}

// daemonRelease is the parker release the machine's daemon should run: this
// binary's version if it is a release, else the latest release ("" if
// GitHub can't be reached: the VM keeps what it has).
func daemonRelease() string {
	if releaseVersion.MatchString(version) {
		return version
	}
	c := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Head("https://github.com/Ranjandas/parker/releases/latest")
	if err != nil {
		return ""
	}
	resp.Body.Close()
	// .../releases/tag/vX.Y.Z
	v := path.Base(resp.Header.Get("Location"))
	if !releaseVersion.MatchString(v) {
		return ""
	}
	return v
}

var releaseVersion = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

func stopMachine(m machine.Machine) error {
	pid := m.PID()
	if pid == 0 {
		say("parker machine is not running")
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	for range 450 {
		if !m.Running() {
			say("stopped parker machine")
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("parker machine (pid %d) did not stop within 45s", pid)
}

func machineStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Shut the machine down (labs in it stop too)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := machine.Default()
			if err != nil {
				return err
			}
			return stopMachine(m)
		},
	}
}

func machineRmCmd() *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "rm",
		Short: "Delete the machine and its disk (the downloaded image stays cached)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := machine.Default()
			if err != nil {
				return err
			}
			if _, err := os.Stat(m.Dir); err != nil {
				say("no parker machine")
				return nil
			}
			if m.Running() {
				if !force {
					return errors.New("parker machine is running: stop it first, or use --force")
				}
				if err := stopMachine(m); err != nil {
					return err
				}
			}
			if err := os.RemoveAll(m.Dir); err != nil {
				return err
			}
			say("removed parker machine")
			return nil
		},
	}
	c.Flags().BoolVarP(&force, "force", "f", false, "stop the machine first if it is running")
	return c
}

func machineSSHCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh [-- COMMAND...]",
		Short: "Open a shell in the machine, or run a command there",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := machine.Default()
			if err != nil {
				return err
			}
			r, ok := m.ReadReady()
			if !m.Running() || !ok {
				return errors.New("parker machine is not running: parker machine start")
			}
			sh := exec.Command("ssh", append(m.SSHArgs(r.IP), args...)...)
			sh.Stdin, sh.Stdout, sh.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := sh.Run(); err != nil {
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					os.Exit(ee.ExitCode())
				}
				return err
			}
			return nil
		},
	}
}
