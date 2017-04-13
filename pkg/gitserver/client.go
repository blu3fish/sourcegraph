package gitserver

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/neelance/chanrpc"
	"github.com/neelance/chanrpc/chanrpcutil"
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/prometheus/client_golang/prometheus"
	"sourcegraph.com/sourcegraph/sourcegraph/pkg/actor"
	sourcegraph "sourcegraph.com/sourcegraph/sourcegraph/pkg/api"
	"sourcegraph.com/sourcegraph/sourcegraph/pkg/env"
	"sourcegraph.com/sourcegraph/sourcegraph/pkg/github"
	"sourcegraph.com/sourcegraph/sourcegraph/pkg/gitserver/protocol"
	"sourcegraph.com/sourcegraph/sourcegraph/pkg/vcs"
)

var gitservers = env.Get("SRC_GIT_SERVERS", "gitserver:3178", "addresses of the remote gitservers")

// DefaultClient is the default Client. Unless overwritten it is connected to servers specified by SRC_GIT_SERVERS.
var DefaultClient = &Client{Addrs: strings.Fields(gitservers)}

// Client is a gitserver client.
type Client struct {
	Addrs       []string
	NoCreds     bool
	servers     [](chan<- *protocol.Request)
	connectOnce sync.Once
}

// HasServers returns true if the client is configured with servers to access.
func (c *Client) HasServers() bool {
	return len(c.servers) > 0
}

func (c *Client) connect() {
	for _, addr := range c.Addrs {
		requestsChan := make(chan *protocol.Request, 100)
		c.servers = append(c.servers, requestsChan)

		go func(addr string) {
			for {
				err := chanrpc.DialAndDeliver(addr, requestsChan)
				log.Printf("gitserver: DialAndDeliver error: %v", err)
				time.Sleep(time.Second)
			}
		}(addr)
	}
}

func (c *Cmd) sendExec(ctx context.Context) (_ *protocol.ExecReply, errRes error) {
	c.client.connectOnce.Do(c.client.connect)

	repoURI := protocol.NormalizeRepo(c.Repo.URI)

	span, ctx := opentracing.StartSpanFromContext(ctx, "Client.sendExec")
	defer func() {
		if errRes != nil {
			ext.Error.Set(span, true)
			span.SetTag("err", errRes.Error())
		}
		span.Finish()
	}()
	span.SetTag("request", "Exec")
	span.SetTag("repo", c.Repo)
	span.SetTag("args", c.Args[1:])

	// Check that ctx is not expired.
	if err := ctx.Err(); err != nil {
		deadlineExceededCounter.Inc()
		return nil, err
	}

	opt := &vcs.RemoteOpts{}
	// 🚨 SECURITY: Only send credentials to gitserver if we know that the repository is private. This 🚨
	// is to avoid fetching private commits while our access checks still assume that the repository
	// is public. In that case better fail fetching those commits until the DB got updated.
	if github.IsRepoAndShouldCheckPermissions(repoURI) && !c.client.NoCreds && c.Repo.Private {
		actor := actor.FromContext(ctx)
		if actor.GitHubToken != "" {
			opt.HTTPS = &vcs.HTTPSConfig{
				User: "x-oauth-token", // User is unused by GitHub, but provide a non-empty value to satisfy git.
				Pass: actor.GitHubToken,
			}
		}
	}

	sum := md5.Sum([]byte(repoURI))
	serverIndex := binary.BigEndian.Uint64(sum[:]) % uint64(len(c.client.servers))
	replyChan := make(chan *protocol.ExecReply, 1)
	c.client.servers[serverIndex] <- &protocol.Request{Exec: &protocol.ExecRequest{
		Repo:           repoURI,
		EnsureRevision: c.EnsureRevision,
		Args:           c.Args[1:],
		Opt:            opt,
		NoAutoUpdate:   c.Repo.Private && c.client.NoCreds,
		Stdin:          chanrpcutil.ToChunks(nil),
		ReplyChan:      replyChan,
	}}
	reply, ok := <-replyChan
	if !ok {
		return nil, errRPCFailed
	}

	if reply.RepoNotFound {
		return nil, vcs.RepoNotExistError{}
	}

	return reply, nil
}

var errRPCFailed = errors.New("gitserver: rpc failed")

var deadlineExceededCounter = prometheus.NewCounter(prometheus.CounterOpts{
	Namespace: "src",
	Subsystem: "gitserver",
	Name:      "client_deadline_exceeded",
	Help:      "Times that Client.sendExec() returned context.DeadlineExceeded",
})

func init() {
	prometheus.MustRegister(deadlineExceededCounter)
}

// Cmd represents a command to be executed remotely.
type Cmd struct {
	client *Client

	Args           []string
	Repo           *sourcegraph.Repo
	EnsureRevision string
	ExitStatus     int
}

// Command creates a new Cmd. Command name must be 'git',
// otherwise it panics.
func (c *Client) Command(name string, arg ...string) *Cmd {
	if name != "git" {
		panic("gitserver: command name must be 'git'")
	}
	return &Cmd{
		client: c,
		Args:   append([]string{"git"}, arg...),
	}
}

// DividedOutput runs the command and returns its standard output and standard error.
func (c *Cmd) DividedOutput(ctx context.Context) ([]byte, []byte, error) {
	reply, err := c.sendExec(ctx)
	if err != nil {
		return nil, nil, err
	}

	if reply.CloneInProgress {
		return nil, nil, vcs.RepoNotExistError{CloneInProgress: true}
	}
	stdout := chanrpcutil.ReadAll(reply.Stdout)
	stderr := chanrpcutil.ReadAll(reply.Stderr)

	processResult, ok := <-reply.ProcessResult
	if !ok {
		return nil, nil, errors.New("connection to gitserver lost")
	}
	if processResult.Error != "" {
		err = errors.New(processResult.Error)
	}
	c.ExitStatus = processResult.ExitStatus

	return <-stdout, <-stderr, err
}

// Run starts the specified command and waits for it to complete.
func (c *Cmd) Run(ctx context.Context) error {
	_, _, err := c.DividedOutput(ctx)
	return err
}

// Output runs the command and returns its standard output.
func (c *Cmd) Output(ctx context.Context) ([]byte, error) {
	stdout, _, err := c.DividedOutput(ctx)
	return stdout, err
}

// CombinedOutput runs the command and returns its combined standard output and standard error.
func (c *Cmd) CombinedOutput(ctx context.Context) ([]byte, error) {
	stdout, stderr, err := c.DividedOutput(ctx)
	return append(stdout, stderr...), err
}

// StdoutReader returns an io.ReadCloser of stdout of c. If the command has a
// non-zero return value, Read returns a non io.EOF error. Do not pass in a
// started command.
func StdoutReader(ctx context.Context, c *Cmd) (io.ReadCloser, error) {
	reply, err := c.sendExec(ctx)
	if err != nil {
		return nil, err
	}

	if reply.CloneInProgress {
		return nil, vcs.RepoNotExistError{CloneInProgress: true}
	}

	// Ignore stderr
	go chanrpcutil.Drain(reply.Stderr)

	return &cmdReader{
		c:     c,
		reply: reply,
	}, nil
}

type cmdReader struct {
	c     *Cmd
	reply *protocol.ExecReply
	err   error
	// If we read too many bytes, we store the extra bytes here
	buf []byte
}

func (c *cmdReader) Read(p []byte) (n int, err error) {
	if c.err != nil {
		return 0, c.err
	}

	// First check if we have already buffered bytes to give
	n = copy(p, c.buf)
	if n > 0 {
		c.buf = c.buf[n:]
	}

	// Try to fill up p
	for n < len(p) {
		var ok bool
		c.buf, ok = <-c.reply.Stdout
		if !ok {
			break
		}
		nw := copy(p[n:], c.buf)
		n += nw
		c.buf = c.buf[nw:]
	}

	if n != 0 {
		return n, nil
	}

	defer func() { c.err = err }()
	processResult, ok := <-c.reply.ProcessResult
	if !ok {
		return 0, errors.New("connection to gitserver lost")
	}
	if processResult.Error != "" {
		return 0, errors.New(processResult.Error)
	}
	if processResult.ExitStatus != 0 {
		return 0, fmt.Errorf("non-zero exit code: %d", processResult.ExitStatus)
	}
	return 0, io.EOF
}

func (c *cmdReader) Close() error {
	// Drain
	go func() {
		chanrpcutil.Drain(c.reply.Stdout)
		<-c.reply.ProcessResult
	}()
	return nil
}
