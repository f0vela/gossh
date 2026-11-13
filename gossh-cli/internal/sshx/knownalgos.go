package sshx

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// knownHostKeyAlgorithms lists the host key algorithms known_hosts already
// holds for addr ("host:port"), in preference order. Offering only those
// makes the server present a key we can verify; otherwise a server that
// prefers another key type than the recorded one looks like "host key
// changed" even though nothing is wrong (OpenSSH does the same). Empty
// when the host is unknown: defaults apply and the user is asked.
func knownHostKeyAlgorithms(khPath, addr string) []string {
	data, err := os.ReadFile(khPath)
	if err != nil {
		return nil
	}
	want := knownhosts.Normalize(addr)
	var algos []string
	add := func(a ...string) {
		for _, x := range a {
			for _, have := range algos {
				if have == x {
					goto next
				}
			}
			algos = append(algos, x)
		next:
		}
	}
	for rest := data; len(bytes.TrimSpace(rest)) > 0; {
		marker, hosts, key, _, r, err := ssh.ParseKnownHosts(rest)
		if err != nil {
			break // EOF or malformed tail: keep what was found
		}
		rest = r
		if marker != "" || !hostMatches(hosts, want) {
			continue
		}
		switch key.Type() {
		case ssh.KeyAlgoRSA:
			add(ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256)
		default:
			add(key.Type())
		}
	}
	return algos
}

// hostMatches reports whether any host field of a known_hosts line is the
// normalized address: plain, or hashed (|1|salt|hmac-sha1).
func hostMatches(hosts []string, want string) bool {
	for _, h := range hosts {
		if !strings.HasPrefix(h, "|1|") {
			if h == want {
				return true
			}
			continue
		}
		parts := strings.Split(h, "|") // "", "1", salt, hash
		if len(parts) != 4 {
			continue
		}
		salt, err1 := base64.StdEncoding.DecodeString(parts[2])
		sum, err2 := base64.StdEncoding.DecodeString(parts[3])
		if err1 != nil || err2 != nil {
			continue
		}
		mac := hmac.New(sha1.New, salt)
		mac.Write([]byte(want))
		if hmac.Equal(mac.Sum(nil), sum) {
			return true
		}
	}
	return false
}
