package sshx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"gossh-go/internal/store"
)

// Prompt asks the user one keyboard-interactive question (an OTP code, a
// second factor, a changed password…). ok=false cancels authentication.
type Prompt func(instruction, question string, echo bool) (answer string, ok bool)

// authMethods builds the ssh.AuthMethod list for the profile, falling back
// across methods like the Rust version does.
func authMethods(p store.ConnectionProfile, prompt Prompt) ([]ssh.AuthMethod, error) {
	hasPass := strings.TrimSpace(p.Password) != ""
	hasKey := strings.TrimSpace(p.PrivateKeyPath) != ""

	var passAuth, keyAuth ssh.AuthMethod
	if hasPass {
		passAuth = ssh.Password(p.Password)
	}
	if hasKey {
		k, err := keySigner(p)
		if err != nil {
			return nil, err
		}
		keyAuth = ssh.PublicKeys(k)
	}
	kbInteractive := func() ssh.AuthMethod {
		if !hasPass && prompt == nil {
			return nil
		}
		return ssh.KeyboardInteractive(kbChallenge(p, prompt))
	}

	order := func(methods ...ssh.AuthMethod) []ssh.AuthMethod {
		var out []ssh.AuthMethod
		for _, m := range methods {
			if m != nil {
				out = append(out, m)
			}
		}
		return out
	}

	var methods []ssh.AuthMethod
	switch p.AuthMethod {
	case store.AuthKey:
		methods = order(keyAuth)
	case store.AuthInteractive:
		methods = order(kbInteractive(), passAuth)
	case store.AuthBoth:
		methods = order(keyAuth, passAuth, kbInteractive())
	default: // password
		methods = order(passAuth, kbInteractive())
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("sin credenciales configuradas para autenticación")
	}
	return methods, nil
}

func keySigner(p store.ConnectionProfile) (ssh.Signer, error) {
	path := ExpandTilde(p.PrivateKeyPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("leyendo private key %s: %w", path, err)
	}
	if strings.TrimSpace(p.PrivateKeyPass) != "" {
		return ssh.ParsePrivateKeyWithPassphrase(data, []byte(p.PrivateKeyPass))
	}
	return ssh.ParsePrivateKey(data)
}

// kbChallenge answers keyboard-interactive questions. The stored password
// answers the first hidden "password" question only; anything else (OTP,
// second factor, a retry after a wrong password) goes to the user.
func kbChallenge(p store.ConnectionProfile, prompt Prompt) ssh.KeyboardInteractiveChallenge {
	hasPass := strings.TrimSpace(p.Password) != ""
	usedPass := false
	return func(user, instruction string, questions []string, echos []bool) ([]string, error) {
		answers := make([]string, len(questions))
		for i, q := range questions {
			if hasPass && !usedPass && !echos[i] && looksLikePasswordPrompt(q) {
				answers[i], usedPass = p.Password, true
				continue
			}
			if prompt == nil {
				return nil, fmt.Errorf("el servidor pide %q y no hay forma de preguntar", q)
			}
			a, ok := prompt(instruction, q, echos[i])
			if !ok {
				return nil, errors.New("autenticación cancelada")
			}
			answers[i] = a
		}
		return answers, nil
	}
}

func looksLikePasswordPrompt(q string) bool {
	q = strings.ToLower(q)
	return strings.Contains(q, "password") || strings.Contains(q, "contraseña")
}

// ExpandTilde expands a leading "~/".
func ExpandTilde(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(homeDir(), rest)
	}
	return p
}

// hostKeyCallback verifies the server key against ~/.ssh/known_hosts:
// match → ok; mismatch → hard fail (MITM warning); unknown → user confirm,
// then append.
func hostKeyCallback(p store.ConnectionProfile, khPath string, confirm HostKeyConfirm) (ssh.HostKeyCallback, error) {
	// knownhosts.New fails (and returns a nil callback) when the file is
	// missing, so create it empty first: a first-time user must get the
	// trust prompt, not a nil-func panic.
	if err := os.MkdirAll(filepath.Dir(khPath), 0o700); err != nil {
		return nil, err
	}
	if f, err := os.OpenFile(khPath, os.O_CREATE|os.O_RDONLY, 0o600); err == nil {
		f.Close()
	}
	kh, err := knownhosts.New(khPath)
	if err != nil {
		return nil, fmt.Errorf("no se pudo leer %s: %w", khPath, err)
	}
	addr := net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := kh(hostname, remote, key)
		if err == nil {
			return nil
		}
		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) || len(keyErr.Want) > 0 {
			return fmt.Errorf("¡LA HOST KEY CAMBIÓ para %s!\n"+
				"Posible ataque man-in-the-middle o servidor reinstalado.\n"+
				"Huella recibida: %s\n"+
				"Si confías en el cambio, elimina la entrada de %s y reintenta.",
				addr, ssh.FingerprintSHA256(key), khPath)
		}
		info := HostKeyInfo{
			Host: p.Host, Port: p.Port,
			KeyType:     key.Type(),
			Fingerprint: ssh.FingerprintSHA256(key),
		}
		if !confirm(info) {
			return fmt.Errorf("host key de %s no aceptada; conexión cancelada", addr)
		}
		return appendKnownHost(khPath, addr, key)
	}, nil
}

// appendKnownHost records the key with knownhosts.Line, which normalizes
// "host:port" the way OpenSSH does ([host]:port when not 22).
func appendKnownHost(khPath, addr string, key ssh.PublicKey) error {
	line := knownhosts.Line([]string{addr}, key) + "\n"
	f, err := os.OpenFile(khPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("abriendo %s: %w", khPath, err)
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}
