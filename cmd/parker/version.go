package main

import (
	"context"
	"fmt"
	"regexp"
	"runtime"
	"runtime/debug"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/daemon"
)

// Set by the release build: -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
var (
	version = "dev"
	commit  = ""
	date    = ""
)

// pseudoVersion matches what `go build` stamps on untagged commits.
var pseudoVersion = regexp.MustCompile(`-(0\.)?\d{14}-[0-9a-f]{12}`)

func init() {
	// Plain `go build` / `go install`: take what the toolchain recorded.
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; version == "dev" && v != "" && v != "(devel)" && !pseudoVersion.MatchString(v) {
			version = v
		}
		dirty, fromVCS := false, false
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if commit == "" {
					commit, fromVCS = s.Value, true
				}
			case "vcs.time":
				if date == "" {
					date = s.Value
				}
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if fromVCS && len(commit) > 12 {
			commit = commit[:12]
		}
		if dirty && fromVCS {
			commit += "-dirty"
		}
	}
	daemon.Version = buildID()
}

// buildID names this build for the daemon handshake: the version, plus
// the commit when known, so two dev builds tell each other apart.
func buildID() string {
	if commit == "" {
		return version
	}
	return version + "+" + commit
}

func versionString() string {
	s := "parker " + version
	if commit != "" {
		s += " (commit " + commit
		if date != "" {
			s += ", built " + date
		}
		s += ")"
	}
	return s + " " + runtime.GOOS + "/" + runtime.GOARCH + " " + runtime.Version()
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the client version, and the daemon's if one answers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(versionString())
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			c, ok, err := connectDaemon(ctx)
			if err != nil {
				fmt.Printf("daemon: %v\n", err)
				return nil
			}
			if !ok {
				fmt.Println("daemon: not running")
				return nil
			}
			r, err := c.Version(ctx, connect.NewRequest(&parkerv1.VersionRequest{}))
			if err != nil {
				fmt.Printf("daemon: %v\n", rpcErr(err))
				return nil
			}
			where := target().Host
			if where == "" {
				where = daemon.SocketPath()
			}
			fmt.Printf("daemon: parker %s (pid %d) at %s\n", r.Msg.Version, r.Msg.Pid, where)
			return nil
		},
	}
}
