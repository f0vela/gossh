// Package store manages SSH connection profiles: on-disk AES-256-GCM
// encrypted storage, master key in macOS Keychain, and passphrase-protected
// backup export/import fully compatible with the Rust tuissh format.
package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"golang.org/x/crypto/argon2"
)

const (
	KeyringService = "tuissh"
	KeyringUser    = "data-key"
	DataFile       = "profiles.enc"

	BackupFormat             = "tuissh-backup"
	BackupVersion            = 2
	backupMinVersion         = 1
	MinBackupPassphraseChars = 12
	backupSaltLen            = 16

	// `security` exit status for errSecItemNotFound.
	keychainItemNotFound = 44

	// MaxBackupFileSize bounds a backup read from disk.
	MaxBackupFileSize = 16 << 20
)

// KDFParams are Argon2id costs stored in the backup so they can change later.
type KDFParams struct {
	MCost uint32
	TCost uint32
	PCost uint32
}

var (
	// Current costs for new backups: 64 MiB, 3 passes.
	BackupKDF = KDFParams{MCost: 64 * 1024, TCost: 3, PCost: 1}
	// Costs used by v1 backups (argon2 crate defaults).
	legacyKDF = KDFParams{MCost: 19 * 1024, TCost: 2, PCost: 1}
)

const (
	maxKDFMCost = 512 * 1024
	maxKDFTCost = 10
	maxKDFPCost = 8
)

// AuthMethod mirrors the Rust enum (serde lowercase).
type AuthMethod string

const (
	AuthPassword    AuthMethod = "password"
	AuthKey         AuthMethod = "key"
	AuthBoth        AuthMethod = "both"
	AuthInteractive AuthMethod = "interactive"
)

// ConnectionProfile is serialized identically to the Rust ConnectionProfile,
// so encrypted stores and backups are interchangeable.
type ConnectionProfile struct {
	Name           string     `json:"name"`
	Host           string     `json:"host"`
	Port           uint16     `json:"port"`
	User           string     `json:"user"`
	AuthMethod     AuthMethod `json:"auth_method"`
	Password       string     `json:"password"`
	PrivateKeyPath string     `json:"private_key_path"`
	PrivateKeyPass string     `json:"private_key_pass"`
}

// LoadedProfiles is the result of opening the encrypted store.
type LoadedProfiles struct {
	Profiles []ConnectionProfile
	// Set when the store existed but could not be decrypted and was moved aside.
	Notice string
}

type SecureStore struct {
	dataPath string
}

func New() (*SecureStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	base := filepath.Join(home, "Library", "Application Support", "tuissh")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(base, 0o700)
	return &SecureStore{dataPath: filepath.Join(base, DataFile)}, nil
}

// WithDataPath creates a store backed by an explicit path (tests).
func WithDataPath(path string) *SecureStore { return &SecureStore{dataPath: path} }

func (s *SecureStore) LoadProfiles() (*LoadedProfiles, error) {
	ciphertext, err := os.ReadFile(s.dataPath)
	if errors.Is(err, os.ErrNotExist) {
		return &LoadedProfiles{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("leyendo almacenamiento: %w", err)
	}
	key, err := s.getOrCreateKey()
	if err != nil {
		return nil, err
	}
	plain, err := decrypt(ciphertext, key)
	if err != nil {
		moved, berr := s.backupUnreadableStore()
		if berr != nil {
			return nil, berr
		}
		return &LoadedProfiles{Notice: fmt.Sprintf(
			"No se pudieron descifrar tus perfiles guardados (¿cambió la clave del keychain '%s'?). "+
				"El archivo NO se borró: quedó en %s. Si recuperas la clave, restáuralo como %s.",
			KeyringService, moved, s.dataPath)}, nil
	}
	var profiles []ConnectionProfile
	if err := json.Unmarshal(plain, &profiles); err != nil {
		return nil, fmt.Errorf("parseando perfiles: %w", err)
	}
	return &LoadedProfiles{Profiles: profiles}, nil
}

func (s *SecureStore) SaveProfiles(profiles []ConnectionProfile) error {
	key, err := s.getOrCreateKey()
	if err != nil {
		return err
	}
	plain, err := json.MarshalIndent(profiles, "", "  ")
	if err != nil {
		return err
	}
	enc, err := encrypt(plain, key)
	if err != nil {
		return err
	}
	tmp := strings.TrimSuffix(s.dataPath, ".enc") + ".enc.tmp"
	if err := writePrivate(tmp, enc); err != nil {
		return fmt.Errorf("guardando perfiles: %w", err)
	}
	return os.Rename(tmp, s.dataPath)
}

func (s *SecureStore) backupUnreadableStore() (string, error) {
	moved := strings.TrimSuffix(s.dataPath, ".enc") +
		fmt.Sprintf(".enc.unreadable.%d.bak", time.Now().Unix())
	if err := os.Rename(s.dataPath, moved); err != nil {
		return "", fmt.Errorf("no se pudo respaldar almacenamiento ilegible: %w", err)
	}
	return moved, nil
}

// getOrCreateKey returns the 32-byte master key from the macOS Keychain
// (service/account shared with the Rust app). A key is only created when
// none exists: any other failure (locked keychain, access denied by the
// user) is returned, because replacing the entry would destroy the key of
// the existing encrypted store.
func (s *SecureStore) getOrCreateKey() ([32]byte, error) {
	var zero [32]byte
	raw, err := readKeychainKey()
	switch {
	case err == nil:
		key, derr := decodeKey(strings.TrimSpace(raw))
		if derr == nil {
			return key, nil
		}
		if st, statErr := os.Stat(s.dataPath); statErr == nil && st.Size() > 0 {
			return zero, fmt.Errorf("clave keychain inválida y hay datos cifrados existentes en %s. "+
				"Borra la entrada de keychain del servicio '%s' cuenta '%s' para regenerar",
				s.dataPath, KeyringService, KeyringUser)
		}
		return s.storeNewKey()
	case errors.Is(err, errKeyNotFound):
		return s.storeNewKey()
	}
	return zero, fmt.Errorf("no se pudo leer la clave del keychain: %w", err)
}

var errKeyNotFound = errors.New("clave no encontrada en el keychain")

// readKeychainKey reads the key through the native Keychain API.
//
// A key made by the Rust app, or by an older build through the `security`
// CLI, is not found there. It is read once with the CLI (which that item
// trusts, so silently) and recreated natively: the Keychain trusts the app
// that created an item, so later launches read it without asking for the
// macOS password. Updating the old item in place would keep its ACL, which
// only trusts `security`, and ask on every launch.
func readKeychainKey() (string, error) { return readKeychainKeyFor(KeyringService) }

func readKeychainKeyFor(service string) (string, error) {
	secrets := mygo.Secrets.Service(service)
	b, err := secrets.Get(KeyringUser)
	if err == nil {
		return string(b), nil
	}
	if !errors.Is(err, mygo.ErrSecretNotFound) {
		return "", err
	}
	out, err := runSecurity("find-generic-password", "-s", service, "-a", KeyringUser, "-w")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == keychainItemNotFound {
			return "", errKeyNotFound
		}
		return "", err
	}
	raw := strings.TrimSpace(string(out))
	migrateLegacyKey(service, raw)
	return raw, nil
}

// migrateLegacyKey replaces the CLI-made item with one the app owns. Best
// effort, and never leaves the user without a key: if the native write
// fails after the old item was removed, the key is put back with the CLI.
func migrateLegacyKey(service, raw string) {
	if _, err := runSecurity("delete-generic-password", "-s", service, "-a", KeyringUser); err != nil {
		return // old item stays; it is still readable through the CLI
	}
	if err := mygo.Secrets.Service(service).Set(KeyringUser, []byte(raw)); err != nil {
		_, _ = runSecurity("add-generic-password", "-s", service, "-a", KeyringUser, "-w", raw, "-U")
	}
}

// runSecurity runs the `security` CLI with a deadline, so a Keychain
// prompt nobody answers cannot hang the app.
func runSecurity(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "security", args...).Output()
}

// storeNewKey generates a key and stores it with the native Keychain API,
// which (unlike `security add-generic-password -w`) never puts the key in
// a process argument list.
func (s *SecureStore) storeNewKey() ([32]byte, error) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return key, err
	}
	encoded := base64.StdEncoding.EncodeToString(key[:])
	if err := mygo.Secrets.Service(KeyringService).Set(KeyringUser, []byte(encoded)); err != nil {
		return key, fmt.Errorf("guardando clave en keychain: %w", err)
	}
	return key, nil
}

// BackupEnvelope is the portable passphrase-protected backup JSON.
type BackupEnvelope struct {
	Format        string `json:"format"`
	Version       uint32 `json:"version"`
	KDF           string `json:"kdf"`
	Salt          string `json:"salt"`
	Data          string `json:"data"`
	MCost         uint32 `json:"m_cost,omitempty"`
	TCost         uint32 `json:"t_cost,omitempty"`
	PCost         uint32 `json:"p_cost,omitempty"`
	CreatedAt     uint64 `json:"created_at,omitempty"`
	ProfilesCount int    `json:"profiles_count,omitempty"`
}

// ExportBackup encrypts profiles with a passphrase-derived key.
func ExportBackup(profiles []ConnectionProfile, passphrase string) (string, error) {
	if len([]rune(passphrase)) < MinBackupPassphraseChars {
		return "", fmt.Errorf("la contraseña del backup debe tener al menos %d caracteres",
			MinBackupPassphraseChars)
	}
	salt := make([]byte, backupSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(passphrase), salt, BackupKDF.TCost,
		BackupKDF.MCost, uint8(BackupKDF.PCost), 32)
	plain, err := json.Marshal(profiles)
	if err != nil {
		return "", err
	}
	var keyArr [32]byte
	copy(keyArr[:], key)
	enc, err := encrypt(plain, keyArr)
	if err != nil {
		return "", err
	}
	env := BackupEnvelope{
		Format: BackupFormat, Version: BackupVersion, KDF: "argon2id",
		Salt:  base64.StdEncoding.EncodeToString(salt),
		Data:  base64.StdEncoding.EncodeToString(enc),
		MCost: BackupKDF.MCost, TCost: BackupKDF.TCost, PCost: BackupKDF.PCost,
		CreatedAt: uint64(time.Now().Unix()), ProfilesCount: len(profiles),
	}
	out, err := json.MarshalIndent(&env, "", "  ")
	return string(out), err
}

// ImportBackup decrypts a backup produced by ExportBackup (Go or Rust).
func ImportBackup(jsonData []byte, passphrase string) ([]ConnectionProfile, error) {
	var env BackupEnvelope
	if err := json.Unmarshal(jsonData, &env); err != nil {
		return nil, fmt.Errorf("el archivo no es un backup válido de tuissh")
	}
	if env.Format != BackupFormat {
		return nil, fmt.Errorf("formato de backup no reconocido: %s", env.Format)
	}
	if env.Version < backupMinVersion || env.Version > BackupVersion {
		return nil, fmt.Errorf("versión de backup no soportada: %d", env.Version)
	}
	kdf := legacyKDF
	if env.Version >= 2 {
		kdf = KDFParams{MCost: env.MCost, TCost: env.TCost, PCost: env.PCost}
		if kdf.MCost == 0 || kdf.TCost == 0 || kdf.PCost == 0 ||
			kdf.MCost > maxKDFMCost || kdf.TCost > maxKDFTCost || kdf.PCost > maxKDFPCost {
			return nil, fmt.Errorf("parámetros de derivación de clave fuera de rango en el backup")
		}
	}
	salt, err := base64.StdEncoding.DecodeString(env.Salt)
	if err != nil || len(salt) != backupSaltLen {
		return nil, fmt.Errorf("salt inválido en el backup")
	}
	blob, err := base64.StdEncoding.DecodeString(env.Data)
	if err != nil {
		return nil, fmt.Errorf("datos cifrados inválidos en el backup")
	}
	key := argon2.IDKey([]byte(passphrase), salt, kdf.TCost, kdf.MCost, uint8(kdf.PCost), 32)
	var keyArr [32]byte
	copy(keyArr[:], key)
	plain, err := decrypt(blob, keyArr)
	if err != nil {
		return nil, fmt.Errorf("contraseña incorrecta o backup corrupto")
	}
	var profiles []ConnectionProfile
	if err := json.Unmarshal(plain, &profiles); err != nil {
		return nil, fmt.Errorf("contenido del backup ilegible")
	}
	return profiles, nil
}

// AppendNew returns existing plus the imported profiles that are not
// duplicates (same name+host+port+user).
func AppendNew(existing, imported []ConnectionProfile) []ConnectionProfile {
	seen := map[string]bool{}
	for _, p := range existing {
		seen[dupKey(p)] = true
	}
	out := append([]ConnectionProfile{}, existing...)
	for _, p := range imported {
		if !seen[dupKey(p)] {
			seen[dupKey(p)] = true
			out = append(out, p)
		}
	}
	return out
}

// MergeImported reports how many imported profiles AppendNew would add and
// how many it would skip as duplicates.
func MergeImported(existing, imported []ConnectionProfile) (added int, skipped int) {
	added = len(AppendNew(existing, imported)) - len(existing)
	return added, len(imported) - added
}

func dupKey(p ConnectionProfile) string {
	return fmt.Sprintf("%s|%s|%d|%s", p.Name, p.Host, p.Port, p.User)
}

// WritePrivate writes data owner-only (0600) from the start.
func WritePrivate(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func writePrivate(path string, data []byte) error { return WritePrivate(path, data) }

func encrypt(data []byte, key [32]byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, data, nil), nil
}

func decrypt(data []byte, key [32]byte) ([]byte, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("contenido cifrado inválido")
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, data[:12], data[12:], nil)
}

// decodeKey accepts the same encodings the Rust version tolerates.
func decodeKey(encoded string) ([32]byte, error) {
	var zero [32]byte
	s := strings.TrimSpace(encoded)
	try := func(b []byte) ([32]byte, bool) {
		if len(b) != 32 {
			return zero, false
		}
		var k [32]byte
		copy(k[:], b)
		return k, true
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		if k, ok := try(b); ok {
			return k, nil
		}
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		if k, ok := try(b); ok {
			return k, nil
		}
	}
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		if k, ok := try(b); ok {
			return k, nil
		}
	}
	hexed := strings.TrimPrefix(s, "0x")
	if len(hexed) == 64 {
		if b, err := hex.DecodeString(hexed); err == nil {
			if k, ok := try(b); ok {
				return k, nil
			}
		}
	}
	if len(s) == 32 {
		if k, ok := try([]byte(s)); ok {
			return k, nil
		}
	}
	return zero, fmt.Errorf("clave keychain inválida")
}
