package store

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func argon2IDKeyForTest(pw string, salt []byte) []byte {
	return argon2.IDKey([]byte(pw), salt, legacyKDF.TCost, legacyKDF.MCost, uint8(legacyKDF.PCost), 32)
}

func sample() []ConnectionProfile {
	return []ConnectionProfile{{
		Name: "prod", Host: "example.com", Port: 2222, User: "root",
		AuthMethod: AuthPassword, Password: "s3cr3t",
	}}
}

func TestEncryptRoundtrip(t *testing.T) {
	key := [32]byte{7}
	enc, err := encrypt([]byte("hola mundo"), key)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := decrypt(enc, key)
	if err != nil {
		t.Fatal(err)
	}
	if string(dec) != "hola mundo" {
		t.Fatal("roundtrip failed")
	}
	if _, err := decrypt(enc, [32]byte{1}); err == nil {
		t.Fatal("wrong key must fail")
	}
}

func TestBackupRoundtrip(t *testing.T) {
	jsonStr, err := ExportBackup(Backup{Profiles: sample()}, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ImportBackup([]byte(jsonStr), "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	restored := got.Profiles
	if len(restored) != 1 || restored[0].Password != "s3cr3t" || restored[0].Port != 2222 {
		t.Fatalf("bad restore: %+v", restored)
	}
}

func TestBackupWrongPassphrase(t *testing.T) {
	jsonStr, _ := ExportBackup(Backup{Profiles: sample()}, "correct-passphrase")
	if _, err := ImportBackup([]byte(jsonStr), "incorrect-passphrase"); err == nil {
		t.Fatal("wrong passphrase must fail")
	}
}

func TestBackupShortPassphrase(t *testing.T) {
	if _, err := ExportBackup(Backup{Profiles: sample()}, "short"); err == nil {
		t.Fatal("short passphrase must be rejected")
	}
}

func TestBackupV1EnvelopeStillImports(t *testing.T) {
	// Simulate a v1 file: legacy KDF, no cost fields, short passphrase allowed.
	salt := []byte("0123456789abcdef")
	key := argon2IDKeyForTest("pw", salt)
	var ka [32]byte
	copy(ka[:], key)
	plain := `[{"name":"p","host":"h","port":22,"user":"u","auth_method":"password","password":"s3cr3t","private_key_path":"","private_key_pass":""}]`
	enc, err := encrypt([]byte(plain), ka)
	if err != nil {
		t.Fatal(err)
	}
	env := `{"format":"tuissh-backup","version":1,"kdf":"argon2id","salt":"` +
		b64(salt) + `","data":"` + b64(enc) + `"}`
	restored, err := ImportBackup([]byte(env), "pw")
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Profiles) != 1 || restored.Profiles[0].Password != "s3cr3t" {
		t.Fatal("v1 import failed")
	}
}

func TestBackupRejectsBadKDFCosts(t *testing.T) {
	jsonStr, _ := ExportBackup(Backup{Profiles: sample()}, "correct horse")
	bad := strings.Replace(jsonStr, `"m_cost": 65536`, `"m_cost": 999999999`, 1)
	if _, err := ImportBackup([]byte(bad), "correct horse"); err == nil {
		t.Fatal("out-of-range KDF must fail")
	}
}

func TestWritePrivate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := WritePrivate(p, []byte("x")); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", st.Mode().Perm())
	}
}

func TestMergeImportedSkipsDuplicates(t *testing.T) {
	existing := sample()
	imported := append(sample(), ConnectionProfile{Name: "n2", Host: "h2", Port: 22, User: "u"})
	added, skipped := MergeImported(existing, imported)
	if added != 1 || skipped != 1 {
		t.Fatalf("added=%d skipped=%d", added, skipped)
	}
}

func TestLoadSnippetsMissingFile(t *testing.T) {
	s := WithDataPath(filepath.Join(t.TempDir(), DataFile))
	got, err := s.LoadSnippets()
	if err != nil || len(got) != 0 {
		t.Fatalf("want no snippets and no error, got %v, %v", got, err)
	}
}

func TestBackupCarriesSnippets(t *testing.T) {
	in := Backup{Profiles: sample(), Snippets: []Snippet{{Name: "logs", Text: "tail -f /var/log/syslog", Enter: true}}}
	jsonStr, err := ExportBackup(in, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ImportBackup([]byte(jsonStr), "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Snippets) != 1 || got.Snippets[0] != in.Snippets[0] || len(got.Profiles) != 1 {
		t.Fatalf("bad restore: %+v", got)
	}
	if merged := AppendNewSnippets(in.Snippets, got.Snippets); len(merged) != 1 {
		t.Fatalf("duplicates must be skipped, got %d", len(merged))
	}
}
