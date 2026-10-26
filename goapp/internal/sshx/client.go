// Package sshx wraps golang.org/x/crypto/ssh: connect, authenticate,
// host-key verification against ~/.ssh/known_hosts, interactive PTY shell
// and SFTP file operations. Mirrors the Rust ssh.rs worker contract.
package sshx

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"tuissh-go/internal/store"
)

// HostKeyInfo describes an unknown host key awaiting a trust decision.
type HostKeyInfo struct {
	Host        string
	Port        uint16
	KeyType     string
	Fingerprint string
}

// HostKeyConfirm asks the user whether to trust an unknown host key.
type HostKeyConfirm func(HostKeyInfo) bool

type Client struct {
	conn   *ssh.Client
	sftp   *sftp.Client
	sess   *ssh.Session
	stdin  io.WriteCloser
	stdout io.Reader
	home   string
	done   chan struct{}
	cols   int
	rows   int
}

// Connect establishes the session, verifies the host key, opens SFTP and an
// interactive PTY shell of cols x rows.
func Connect(p store.ConnectionProfile, cols, rows int, confirm HostKeyConfirm, prompt Prompt) (*Client, error) {
	auth, err := authMethods(p, prompt)
	if err != nil {
		return nil, err
	}
	khPath := filepath.Join(homeDir(), ".ssh", "known_hosts")
	callback, err := hostKeyCallback(p, khPath, confirm)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            p.User,
		Auth:            auth,
		HostKeyCallback: callback,
		Timeout:         20 * time.Second,
	}
	addr := net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))
	cfg.HostKeyAlgorithms = knownHostKeyAlgorithms(khPath, addr)
	conn, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("conectando a %s: %w", addr, err)
	}

	c := &Client{conn: conn, done: make(chan struct{})}

	sc, err := sftp.NewClient(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("inicializando SFTP: %w", err)
	}
	c.sftp = sc
	if home, err := sc.Getwd(); err == nil {
		c.home = home
	} else {
		c.home = "/"
	}

	sess, err := conn.NewSession()
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("creando sesión: %w", err)
	}
	modes := ssh.TerminalModes{
		ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400,
		// Disable XON/XOFF on the remote PTY: a stray Ctrl+S must not
		// suspend all remote output.
		ssh.IXON: 0, ssh.IXANY: 0, ssh.IXOFF: 0,
	}
	if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		modes = ssh.TerminalModes{ssh.ECHO: 1}
		if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
			c.Close()
			return nil, fmt.Errorf("solicitando PTY: %w", err)
		}
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		c.Close()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		c.Close()
		return nil, err
	}
	if err := sess.Shell(); err != nil {
		c.Close()
		return nil, fmt.Errorf("iniciando shell remota: %w", err)
	}
	c.sess = sess
	c.stdin = stdin
	c.stdout = stdout
	c.cols, c.rows = cols, rows

	go keepalive(conn, c.done)
	return c, nil
}

func (c *Client) Home() string { return c.home }

// Stdin is the remote shell input (wire to the terminal widget).
func (c *Client) Stdin() io.WriteCloser { return c.stdin }

// Stdout is the remote shell output.
func (c *Client) Stdout() io.Reader { return c.stdout }

// Resize sends a window-change request to the remote PTY.
func (c *Client) Resize(cols, rows int) error {
	if cols == c.cols && rows == c.rows {
		return nil
	}
	c.cols, c.rows = cols, rows
	if c.sess == nil {
		return nil
	}
	return c.sess.WindowChange(rows, cols)
}

// Exists reports whether the remote path exists (to confirm overwrites).
func (c *Client) Exists(remote string) bool {
	_, err := c.sftp.Stat(c.resolveRemotePath(remote))
	return err == nil
}

// MaxEditableSize bounds what ReadRemoteFile loads: the editor holds the
// whole file in memory, and a hostile or huge remote file must not exhaust it.
const MaxEditableSize = 2 << 20

func (c *Client) UploadFile(local, remote string) error {
	src, err := os.Open(local)
	if err != nil {
		return fmt.Errorf("leyendo archivo local %s: %w", local, err)
	}
	defer src.Close()
	rp := c.resolveRemotePath(remote)
	if err := c.ensureRemoteParent(rp); err != nil {
		return err
	}
	f, err := c.sftp.Create(rp)
	if err != nil {
		return fmt.Errorf("creando remoto %s: %w", remote, err)
	}
	if _, err := io.Copy(f, src); err != nil {
		f.Close()
		return fmt.Errorf("subiendo a %s: %w", remote, err)
	}
	return f.Close()
}

// ReadRemoteFile returns the file as text. It refuses files over
// MaxEditableSize and non-UTF-8 content: saving that back through a text
// editor would silently corrupt a binary file.
func (c *Client) ReadRemoteFile(remote string) (string, error) {
	f, err := c.sftp.Open(c.resolveRemotePath(remote))
	if err != nil {
		return "", fmt.Errorf("abriendo remoto %s: %w", remote, err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxEditableSize+1))
	if err != nil {
		return "", err
	}
	if len(b) > MaxEditableSize {
		return "", fmt.Errorf("%s supera %d MiB: demasiado grande para editar", remote, MaxEditableSize>>20)
	}
	if !utf8.Valid(b) {
		return "", fmt.Errorf("%s no es texto UTF-8: no se puede editar sin corromperlo", remote)
	}
	return string(b), nil
}

// WriteRemoteFile replaces the file atomically: content goes to a sibling
// temp file that is renamed over the target, so a dropped connection never
// leaves a truncated file. Symlinks are followed and the mode is kept.
func (c *Client) WriteRemoteFile(remote, content string) error {
	rp := c.resolveRemotePath(remote)
	mode := os.FileMode(0o644)
	if real, err := c.sftp.RealPath(rp); err == nil {
		rp = real
	}
	if st, err := c.sftp.Stat(rp); err == nil {
		mode = st.Mode().Perm()
	}
	if err := c.ensureRemoteParent(rp); err != nil {
		return err
	}
	tmp := rp + ".tuissh-tmp"
	f, err := c.sftp.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("creando remoto %s: %w", remote, err)
	}
	_, werr := f.Write([]byte(content))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = c.sftp.Chmod(tmp, mode)
	}
	if werr != nil {
		_ = c.sftp.Remove(tmp)
		return fmt.Errorf("guardando %s: %w", remote, werr)
	}
	if err := c.sftp.PosixRename(tmp, rp); err != nil {
		// Server without posix-rename: remove + rename. If that fails the
		// complete new content is still in tmp, so it is kept on purpose.
		_ = c.sftp.Remove(rp)
		if err := c.sftp.Rename(tmp, rp); err != nil {
			return fmt.Errorf("guardando %s (contenido íntegro en %s): %w", remote, tmp, err)
		}
	}
	return nil
}

func (c *Client) Close() {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	if c.sftp != nil {
		c.sftp.Close()
	}
	if c.conn != nil {
		c.conn.Close()
	}
}

func (c *Client) resolveRemotePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "." || p == "~" {
		return c.home
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return NormalizePOSIXPath(c.home + "/" + rest)
	}
	if strings.HasPrefix(p, "/") {
		return NormalizePOSIXPath(p)
	}
	return NormalizePOSIXPath(c.home + "/" + p)
}

func (c *Client) ensureRemoteParent(remotePath string) error {
	return c.mkdirAll(posixDir(remotePath))
}

func posixDir(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "/"
}

func (c *Client) mkdirAll(dir string) error {
	if dir == "" || dir == "/" || dir == "." {
		return nil
	}
	cleaned := NormalizePOSIXPath(dir)
	cur := ""
	for _, part := range strings.Split(cleaned, "/") {
		if part == "" {
			continue
		}
		cur += "/" + part
		if _, err := c.sftp.Stat(cur); err != nil {
			_ = c.sftp.Mkdir(cur)
		}
	}
	return nil
}

func keepalive(conn *ssh.Client, done <-chan struct{}) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			// An error here is a transport failure (a refusal is just ok=false):
			// close so the terminal sees EOF instead of hanging forever.
			if _, _, err := conn.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				conn.Close()
				return
			}
		}
	}
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}
