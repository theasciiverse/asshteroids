// Command asshteroids is ASSHTEROIDS in one program: it plays in this
// terminal and, while it runs, lets friends on the local network fly in the
// same field with plain ssh. Computer pilots keep the field busy.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/activeterm"
	"github.com/tomz197/asteroids/internal/draw"
	"github.com/tomz197/asteroids/internal/loop/client"
	loopconfig "github.com/tomz197/asteroids/internal/loop/config"
	"github.com/tomz197/asteroids/internal/loop/server"
	"golang.org/x/term"
)

var version = "dev"

const (
	defaultPort = 2222
	portTries   = 10
)

func main() {
	var (
		serve   = flag.Bool("server", false, "only serve: others join with ssh, nobody plays here")
		offline = flag.Bool("offline", false, "play here without opening the game to the network")
		join    = flag.String("join", "", "join a friend's game at `host[:port]` (runs ssh)")
		demo    = flag.Bool("demo", false, "watch the computer fly your ship; any key ends it")
		port    = flag.Int("port", 0, "SSH port to host on (default 2222, or the next free one)")
		bots    = flag.Int("bots", -1, "computer pilots in the field (default 3, none with -server; up to 8)")
		name    = flag.String("name", "", "your name in the field (default: your login name)")
		showVer = flag.Bool("version", false, "print the version")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: asshteroids [options]\n\n"+
			"With no options, plays in this terminal against computer pilots, and\n"+
			"friends on your network can fly in the same field with plain ssh.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVer {
		fmt.Println("asshteroids", version)
		return
	}

	if *join != "" {
		os.Exit(joinGame(*join))
	}

	stateDir, err := stateDir()
	if err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		fatal(err)
	}

	if *bots < 0 {
		*bots = 3
		if *serve {
			*bots = 0
		}
	}
	if *bots > len(server.BotNames) {
		*bots = len(server.BotNames)
	}

	if *serve {
		// the server's log is for whoever runs it
		runServer(stateDir, *port, *bots)
		return
	}

	// While playing, the log would land in the game, so it goes to
	// $ASSHTEROIDS_LOG or nowhere.
	log.SetOutput(io.Discard)
	if path := os.Getenv("ASSHTEROIDS_LOG"); path != "" {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			log.SetOutput(f)
			log.SetFlags(log.LstdFlags | log.Lmicroseconds)
		}
	}
	log.Printf("asshteroids %s starting offline=%v demo=%v bots=%d", version, *offline, *demo, *bots)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gs := server.NewServer()
	go gs.Run(ctx)
	for i := 0; i < *bots; i++ {
		gs.AddBot(server.BotNames[i])
	}

	var info []string
	var sshServer *ssh.Server
	if !*offline && !*demo {
		s, p, err := listen(gs, stateDir, *port)
		if err != nil {
			log.Printf("not open to the network: %v", err)
			info = []string{"Playing on this machine only (no free port for friends)"}
		} else {
			sshServer = s
			info = joinLines(p)
		}
	}

	player := *name
	if player == "" {
		player = loginName()
	}
	best := loadBest(stateDir)

	enableVT()
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		fatal(fmt.Errorf("this needs a terminal: %w", err))
	}
	opts := client.ClientOptions{
		TermSizeFunc: draw.DefaultTermSizeFunc,
		Username:     sanitizeUsername(player),
		Demo:         *demo,
		NoIdleKick:   true,
		Best:         best,
		OnBest:       func(n int) { saveBest(stateDir, n) },
		Info:         info,
	}
	if *demo {
		// the computer's flying is not this player's best
		opts.Best, opts.OnBest = 0, nil
	}
	c := client.NewClient(gs, bufio.NewReader(os.Stdin), os.Stdout, opts)
	runErr := c.Run()
	_ = term.Restore(fd, oldState)

	if sshServer != nil {
		// tell anyone still flying here that the game is over
		gs.Shutdown(3 * time.Second)
		shutCtx, done := context.WithTimeout(context.Background(), 2*time.Second)
		_ = sshServer.Shutdown(shutCtx)
		done()
	}
	if runErr != nil {
		fatal(runErr)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "asshteroids:", err)
	os.Exit(1)
}

// listen opens the game to ssh players on the first free port from the
// one asked for (2222 by default).
func listen(gs *server.Server, stateDir string, port int) (*ssh.Server, int, error) {
	first, tries := port, 1
	if port == 0 {
		first, tries = defaultPort, portTries
	}
	var lastErr error
	for p := first; p < first+tries; p++ {
		ln, err := net.Listen("tcp", net.JoinHostPort("::", strconv.Itoa(p)))
		if err != nil {
			lastErr = err
			continue
		}
		s, err := newSSHServer(gs, stateDir, p)
		if err != nil {
			ln.Close()
			return nil, 0, err
		}
		go func() {
			if err := s.Serve(ln); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
				log.Printf("ssh server: %v", err)
			}
		}()
		log.Printf("open to the network on port %d", p)
		return s, p, nil
	}
	return nil, 0, lastErr
}

// newSSHServer is upstream's SSH front end on our game server, with the host
// key kept in the state folder.
func newSSHServer(gs *server.Server, stateDir string, port int) (*ssh.Server, error) {
	return wish.NewServer(
		wish.WithAddress(net.JoinHostPort("::", strconv.Itoa(port))),
		wish.WithHostKeyPath(filepath.Join(stateDir, "host_key")),
		wish.WithMiddleware(
			gameMiddleware(gs),
			activeterm.Middleware(),
		),
		ssh.WrapConn(func(ctx ssh.Context, conn net.Conn) net.Conn {
			if tcpConn, ok := conn.(*net.TCPConn); ok {
				_ = tcpConn.SetNoDelay(true)
			}
			return conn
		}),
	)
}

// runServer serves the field to ssh players until it is stopped.
func runServer(stateDir string, port, bots int) {
	if port == 0 {
		port = defaultPort
		if p, err := strconv.Atoi(os.Getenv("SSH_PORT")); err == nil {
			port = p
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	gs := server.NewServer()
	go gs.Run(ctx)
	for i := 0; i < bots; i++ {
		gs.AddBot(server.BotNames[i])
	}
	s, err := newSSHServer(gs, stateDir, port)
	if err != nil {
		log.Fatal(err)
	}
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)
	go func() {
		log.Printf("serving on port %d; join with: ssh -p %d <this machine>", port, port)
		if err := s.ListenAndServe(); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	<-done
	log.Println("shutting down")
	gs.Shutdown(15 * time.Second)
	cancel()
	shutCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	_ = s.Shutdown(shutCtx)
}

// gameMiddleware runs a game client for each ssh session (upstream's
// cmd/ssh, on a given server).
func gameMiddleware(gs *server.Server) wish.Middleware {
	return func(next ssh.Handler) ssh.Handler {
		return func(sess ssh.Session) {
			pty, winCh, ok := sess.Pty()
			if !ok {
				fmt.Fprintln(sess, "This needs a terminal: connect with ssh -t")
				return
			}
			log.Printf("ssh session user=%q term=%s size=%dx%d from=%s",
				sess.User(), pty.Term, pty.Window.Width, pty.Window.Height, sess.RemoteAddr())
			size := &sizeTracker{width: pty.Window.Width, height: pty.Window.Height}
			go func() {
				for win := range winCh {
					size.update(win.Width, win.Height)
				}
			}()
			c := client.NewClient(gs, bufio.NewReader(sess), sess, client.ClientOptions{
				TermSizeFunc: size.get,
				Username:     sanitizeUsername(sess.User()),
			})
			if err := c.Run(); err != nil {
				log.Printf("ssh session %q: %v", sess.User(), err)
			}
			next(sess)
		}
	}
}

type sizeTracker struct {
	mu            sync.Mutex
	width, height int
}

func (s *sizeTracker) update(w, h int) {
	s.mu.Lock()
	s.width, s.height = w, h
	s.mu.Unlock()
}

func (s *sizeTracker) get() (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.width, s.height, nil
}

// joinGame runs ssh to a friend's game.
func joinGame(addr string) int {
	host, port := addr, strconv.Itoa(defaultPort)
	if h, p, err := net.SplitHostPort(addr); err == nil {
		host, port = h, p
	}
	cmd := exec.Command("ssh", "-t", "-p", port,
		"-o", "StrictHostKeyChecking=accept-new", "-o", "LogLevel=ERROR", host)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "asshteroids: could not run ssh:", err)
		return 1
	}
	return 0
}

// joinLines tells friends how to join, for the top of the start screen.
func joinLines(port int) []string {
	addrs := localAddresses()
	if len(addrs) == 0 {
		return []string{fmt.Sprintf("Open to your network: ssh -p %d <this machine>", port)}
	}
	return []string{fmt.Sprintf("Friends on your network join with: ssh -p %d %s", port, addrs[0])}
}

// localAddresses lists this machine's private IPv4 addresses (from SSHTron).
func localAddresses() []string {
	var out []string
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		// container and virtual machine bridges are not the local network
		if strings.HasPrefix(iface.Name, "docker") || strings.HasPrefix(iface.Name, "br-") ||
			strings.HasPrefix(iface.Name, "veth") || strings.HasPrefix(iface.Name, "virbr") {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if ok && ipnet.IP.To4() != nil && ipnet.IP.IsPrivate() {
				out = append(out, ipnet.IP.String())
			}
		}
	}
	return out
}

// stateDir holds the host key and the best score:
// $XDG_STATE_HOME/asshteroids (~/.local/state/asshteroids),
// ~/Library/Application Support/asshteroids on macOS,
// %LOCALAPPDATA%\asshteroids on Windows.
func stateDir() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "asshteroids"), nil
	case "windows":
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			return filepath.Join(dir, "asshteroids"), nil
		}
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "asshteroids"), nil
	}
	if dir := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "asshteroids"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "asshteroids"), nil
}

type record struct {
	Best int `json:"best"`
}

func loadBest(dir string) int {
	var r record
	if b, err := os.ReadFile(filepath.Join(dir, "best.json")); err == nil {
		_ = json.Unmarshal(b, &r)
	}
	return r.Best
}

func saveBest(dir string, best int) {
	b, _ := json.Marshal(record{Best: best})
	tmp := filepath.Join(dir, "best.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err == nil {
		_ = os.Rename(tmp, filepath.Join(dir, "best.json"))
	}
	log.Printf("new best %d saved", best)
}

func loginName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		name := u.Username
		// Windows gives DOMAIN\name
		if i := strings.LastIndexAny(name, `\/`); i >= 0 {
			name = name[i+1:]
		}
		return name
	}
	for _, v := range []string{"USER", "USERNAME", "LOGNAME"} {
		if n := os.Getenv(v); n != "" {
			return n
		}
	}
	return "pilot"
}

// sanitizeUsername keeps a name printable and short (upstream's cmd/ssh).
func sanitizeUsername(raw string) string {
	var b strings.Builder
	count := 0
	for _, r := range raw {
		if !unicode.IsGraphic(r) {
			continue
		}
		if count >= loopconfig.MaxUsernameLength {
			break
		}
		b.WriteRune(r)
		count++
	}
	return strings.TrimSpace(b.String())
}
