package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"gossh-go/internal/store"
)

func testKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// A missing known_hosts must prompt (not panic on a nil callback), persist
// the accepted key, and then reject a different key as a changed host key.
func TestHostKeyCallbackFirstUseThenMismatch(t *testing.T) {
	kh := filepath.Join(t.TempDir(), ".ssh", "known_hosts")
	p := store.ConnectionProfile{Host: "example.test", Port: 2222}
	asked := 0
	cb, err := hostKeyCallback(p, kh, func(HostKeyInfo) bool { asked++; return true })
	if err != nil {
		t.Fatal(err)
	}
	remote := &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 2222}
	key := testKey(t)
	if err := cb("example.test:2222", remote, key); err != nil || asked != 1 {
		t.Fatalf("first use: err=%v asked=%d", err, asked)
	}
	data, _ := os.ReadFile(kh)
	if !strings.HasPrefix(string(data), "[example.test]:2222 ") {
		t.Fatalf("unexpected known_hosts line: %q", data)
	}
	if st, _ := os.Stat(kh); st.Mode().Perm() != 0o600 {
		t.Fatalf("known_hosts mode %v", st.Mode().Perm())
	}

	cb, _ = hostKeyCallback(p, kh, func(HostKeyInfo) bool { asked++; return true })
	if err := cb("example.test:2222", remote, key); err != nil || asked != 1 {
		t.Fatalf("known key must not prompt: err=%v asked=%d", err, asked)
	}
	err = cb("example.test:2222", remote, testKey(t))
	if err == nil || !strings.Contains(err.Error(), "CAMBIÓ") || asked != 1 {
		t.Fatalf("changed key must hard-fail without prompting: err=%v asked=%d", err, asked)
	}
}

func TestHostKeyDeclined(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	p := store.ConnectionProfile{Host: "h.test", Port: 22}
	cb, _ := hostKeyCallback(p, kh, func(HostKeyInfo) bool { return false })
	if err := cb("h.test:22", &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 22}, testKey(t)); err == nil {
		t.Fatal("declined key must fail")
	}
	if data, _ := os.ReadFile(kh); len(data) != 0 {
		t.Fatalf("declined key was written: %q", data)
	}
}

func TestKnownHostKeyAlgorithms(t *testing.T) {
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	ed := testKey(t)
	line := knownhosts.Line([]string{"srv.test:22"}, ed) + "\n" +
		knownhosts.Line([]string{"[srv.test]:2222"}, ed) + "\n" +
		knownhosts.Line([]string{knownhosts.HashHostname("hashed.test")}, ed) + "\n"
	if err := os.WriteFile(kh, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string]int{
		"srv.test:22": 1, "srv.test:2222": 1, "hashed.test:22": 1, "other.test:22": 0, "srv.test:2200": 0,
	} {
		got := knownHostKeyAlgorithms(kh, addr)
		if len(got) != want || (want == 1 && got[0] != ssh.KeyAlgoED25519) {
			t.Errorf("%s: got %v", addr, got)
		}
	}
	if got := knownHostKeyAlgorithms(filepath.Join(dir, "missing"), "srv.test:22"); got != nil {
		t.Errorf("missing file: %v", got)
	}
}

func TestKeyboardInteractiveRoutesPrompts(t *testing.T) {
	p := store.ConnectionProfile{AuthMethod: store.AuthInteractive, Password: "pw"}
	var asked []string
	prompt := func(_, q string, _ bool) (string, bool) { asked = append(asked, q); return "123456", true }
	got, err := kbChallenge(p, prompt)("u", "", []string{"Password: ", "Verification code: "}, []bool{false, true})
	if err != nil || got[0] != "pw" || got[1] != "123456" || len(asked) != 1 {
		t.Fatalf("got=%v err=%v asked=%v", got, err, asked)
	}
	// A retry after a wrong password must ask the user, not resend the same one.
	ch := kbChallenge(p, prompt)
	_, _ = ch("u", "", []string{"Password: "}, []bool{false})
	got, _ = ch("u", "", []string{"Password: "}, []bool{false})
	if got[0] != "123456" {
		t.Fatalf("second password prompt must go to the user, got %v", got)
	}
	if _, err := kbChallenge(p, nil)("u", "", []string{"Code: "}, []bool{true}); err == nil {
		t.Fatal("unanswerable prompt must fail")
	}
	if _, err := kbChallenge(p, func(_, _ string, _ bool) (string, bool) { return "", false })("u", "", []string{"Code: "}, []bool{true}); err == nil {
		t.Fatal("cancelled prompt must fail")
	}
}
