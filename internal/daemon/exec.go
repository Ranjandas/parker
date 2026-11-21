package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"connectrpc.com/connect"
	"github.com/creack/pty"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/lab"
	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/supervisor"
)

func (s *Server) Exec(ctx context.Context, stream *connect.BidiStream[parkerv1.ExecRequest, parkerv1.ExecResponse]) error {
	first, err := stream.Receive()
	if err != nil {
		return err
	}
	st := first.GetStart()
	if st == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("first message must be start"))
	}
	low, err := lab.LoadTopology(st.Lab)
	if err != nil {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("lab %q: %w", st.Lab, err))
	}
	node, err := lab.ResolveNode(low, st.Node)
	if err != nil {
		return connect.NewError(connect.CodeNotFound, err)
	}
	argv := st.Argv
	if len(argv) == 0 {
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		argv = []string{sh}
	}
	home := supervisor.NodeDir(lab.RunDir(st.Lab), node)
	path, env := lab.ExecEnv(low, node, append([]string{"HOME=" + home}, st.Env...), io.Discard)
	// HOME is the node's dir; the shell still loads the daemon user's
	// ~/.bashrc before naming the node.
	argv, env = lab.NodeShell(low, node, argv, env, os.Getenv("HOME"))
	bin, err := lab.LookPath(argv[0], path)
	if err != nil {
		return connect.NewError(connect.CodeNotFound, err)
	}
	cmd := exec.CommandContext(ctx, bin, argv[1:]...)
	cmd.Args[0] = argv[0]
	cmd.Env = env
	cmd.Dir = home
	nsPath := netsetup.NetnsPath(st.Lab, node)

	var sendMu sync.Mutex
	send := func(r *parkerv1.ExecResponse) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(r)
	}
	pump := func(r io.Reader, wrap func([]byte) *parkerv1.ExecResponse, wg *sync.WaitGroup) {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				if send(wrap(append([]byte(nil), buf[:n]...))) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	stdout := func(b []byte) *parkerv1.ExecResponse {
		return &parkerv1.ExecResponse{Msg: &parkerv1.ExecResponse_Stdout{Stdout: b}}
	}
	stderr := func(b []byte) *parkerv1.ExecResponse {
		return &parkerv1.ExecResponse{Msg: &parkerv1.ExecResponse_Stderr{Stderr: b}}
	}

	var out sync.WaitGroup
	var stdin io.WriteCloser
	var ptmx *os.File
	if st.Tty {
		size := &pty.Winsize{Rows: 24, Cols: 80}
		if st.Size != nil && st.Size.Rows > 0 {
			size = &pty.Winsize{Rows: uint16(st.Size.Rows), Cols: uint16(st.Size.Cols)}
		}
		err = supervisor.InNetns(nsPath, func() error {
			var err error
			ptmx, err = pty.StartWithSize(cmd, size)
			return err
		})
		if err != nil {
			return connect.NewError(connect.CodeFailedPrecondition, lab.PrivHint(err))
		}
		defer ptmx.Close()
		stdin = ptmx
		out.Add(1)
		go pump(ptmx, stdout, &out)
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		in, err1 := cmd.StdinPipe()
		o, err2 := cmd.StdoutPipe()
		e, err3 := cmd.StderrPipe()
		if err := errors.Join(err1, err2, err3); err != nil {
			return err
		}
		if err := supervisor.InNetns(nsPath, cmd.Start); err != nil {
			return connect.NewError(connect.CodeFailedPrecondition, lab.PrivHint(err))
		}
		stdin = in
		out.Add(2)
		go pump(o, stdout, &out)
		go pump(e, stderr, &out)
	}

	// Client -> process.
	go func() {
		for {
			msg, err := stream.Receive()
			if err != nil {
				if !st.Tty {
					stdin.Close()
				}
				return
			}
			switch m := msg.Msg.(type) {
			case *parkerv1.ExecRequest_Stdin:
				_, _ = stdin.Write(m.Stdin)
			case *parkerv1.ExecRequest_Resize:
				if ptmx != nil {
					_ = pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(m.Resize.Rows), Cols: uint16(m.Resize.Cols)})
				}
			case *parkerv1.ExecRequest_StdinEof:
				if st.Tty {
					_, _ = stdin.Write([]byte{4}) // ^D
				} else {
					stdin.Close()
				}
			}
		}
	}()

	if !st.Tty {
		out.Wait() // pipes close when the process (and its children) exit
	}
	werr := cmd.Wait()
	if st.Tty {
		out.Wait() // reads end with EIO once the child side is gone
	}
	code := 0
	var ee *exec.ExitError
	if errors.As(werr, &ee) {
		code = ee.ExitCode()
		if code < 0 {
			code = 128 + int(ee.Sys().(syscall.WaitStatus).Signal())
		}
	} else if werr != nil {
		return werr
	}
	return send(&parkerv1.ExecResponse{Msg: &parkerv1.ExecResponse_ExitCode{ExitCode: int32(code)}})
}
