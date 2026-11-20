package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ranjandas/parker/internal/daemon"
	"github.com/Ranjandas/parker/internal/spec"
	"github.com/Ranjandas/parker/internal/webui"
)

func uiCmd() *cobra.Command {
	var listen string
	var noOpen bool
	c := &cobra.Command{
		Use:   "ui [FILE|LAB]",
		Short: "Open the web UI in your browser, for a local or remote daemon",
		Long: "Serves the web UI on a loopback address and opens it in your browser. The UI talks\n" +
			"to the same daemon as the CLI (the local socket, or --host / PARKER_HOST with\n" +
			"PARKER_TOKEN), so a remote daemon needs no browser certificate exception.\n\n" +
			"The link carries a one-time sign-in token in its #fragment, which the browser\n" +
			"never sends to a server; the page swaps it for a session cookie and drops it.\n" +
			"Web UIs inside the lab (the Consul UI, service ports) open from the UI's Open\n" +
			"links through the daemon, each on its own origin under *.parker.localhost.\n" +
			"Stop with Ctrl-C.\n\n" +
			"A daemon started with --listen also serves the UI itself at https://HOST:PORT/;\n" +
			"sign in there with the token from `parker daemon credentials`.",
		Example: "  parker ui\n  parker ui examples/mesh-federation.yaml\n  parker --host https://lab-vm:7443 ui --no-open",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lab := ""
			if len(args) == 1 {
				var err error
				if lab, err = labName(args[0]); err != nil {
					return err
				}
			}
			ctx, stop := signalContext()
			defer stop()
			if _, err := ensureDaemon(ctx); err != nil {
				return err
			}
			api, err := apiProxy(target())
			if err != nil {
				return err
			}
			token := webui.NewToken()
			ln, err := net.Listen("tcp", listen)
			if err != nil {
				return err
			}
			srv := &http.Server{
				Handler: webui.New(webui.Options{API: api, Token: token, Where: daemonWhere(), Version: version,
					// The builder's Run: as `parker up` would, from here.
					Up: func(ctx context.Context, low *spec.File, w io.Writer) error {
						return bringUp(ctx, low, upOptions{out: w, quiet: true})
					}}),
				ReadHeaderTimeout: 10 * time.Second,
				BaseContext:       func(net.Listener) context.Context { return ctx },
			}
			errc := make(chan error, 1)
			go func() { errc <- srv.Serve(ln) }()

			link := uiURL(ln.Addr().String(), token, lab)
			fmt.Printf("parker web UI on http://%s (daemon: %s)\n", ln.Addr(), daemonWhere())
			fmt.Printf("open: %s\n", link)
			if !noOpen {
				if err := openBrowser(link); err != nil {
					fmt.Printf("could not open a browser (%v); open the link above\n", err)
				}
			}
			fmt.Println("Ctrl-C to stop")
			select {
			case <-ctx.Done():
			case err := <-errc:
				if !errors.Is(err, http.ErrServerClosed) {
					return err
				}
			}
			sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = srv.Shutdown(sctx)
			return nil
		},
	}
	c.Flags().StringVar(&listen, "listen", "127.0.0.1:0", "address for the UI (keep it on loopback: anyone who reaches it can sign in with the link)")
	c.Flags().BoolVar(&noOpen, "no-open", false, "print the link instead of opening a browser")
	return c
}

// apiProxy forwards the UI's API calls to the daemon, streaming responses
// (logs, scenario runs) as they arrive.
func apiProxy(t daemon.Target) (http.Handler, error) {
	rt, base, err := t.Transport()
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			pr.Out.Host = u.Host
			pr.Out.Header.Del("Origin")
		},
		Transport:     rt,
		FlushInterval: -1,
	}, nil
}

// uiURL is the sign-in link: the token rides in the fragment.
func uiURL(addr, token, lab string) string {
	f := url.Values{"token": {token}}
	if lab != "" {
		f.Set("lab", lab)
	}
	return "http://" + addr + "/#" + f.Encode()
}

func openBrowser(link string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", link)
	default:
		cmd = exec.Command("xdg-open", link)
	}
	return cmd.Start()
}
