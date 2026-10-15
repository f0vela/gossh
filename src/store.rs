use std::fs;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use aes_gcm::aead::{Aead, KeyInit};
use aes_gcm::{Aes256Gcm, Nonce};
use anyhow::{anyhow, bail, Context, Result};
use argon2::{Algorithm, Argon2, Params, Version};
use base64::{
    engine::general_purpose::{STANDARD as B64, URL_SAFE, URL_SAFE_NO_PAD},
    Engine as _,
};
use keyring::Entry;
use rand::RngCore;
use serde::{Deserialize, Serialize};

const KEYRING_SERVICE: &str = "tuissh";
const KEYRING_USER: &str = "data-key";
const DATA_FILE: &str = "profiles.enc";

/// Format identifier written into every backup file.
pub const BACKUP_FORMAT: &str = "tuissh-backup";
/// Current backup format version.
pub const BACKUP_VERSION: u32 = 1;
/// Size of the random salt used for the passphrase KDF.
const BACKUP_SALT_LEN: usize = 16;

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum AuthMethod {
    Password,
    Key,
    Both,
    Interactive,
}

impl Default for AuthMethod {
    fn default() -> Self {
        Self::Password
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct ConnectionProfile {
    pub name: String,
    pub host: String,
    pub port: u16,
    pub user: String,
    #[serde(default)]
    pub auth_method: AuthMethod,
    #[serde(default)]
    pub password: String,
    #[serde(default)]
    pub private_key_path: String,
    #[serde(default)]
    pub private_key_pass: String,
}

pub struct SecureStore {
    data_path: PathBuf,
}

impl SecureStore {
    pub fn new() -> Result<Self> {
        let cfg = dirs::config_dir().ok_or_else(|| anyhow!("no se pudo detectar config dir"))?;
        let base = cfg.join("tuissh");
        fs::create_dir_all(&base).context("creando directorio de configuración")?;
        let _ = set_private_dir_permissions(&base);
        Ok(Self {
            data_path: base.join(DATA_FILE),
        })
    }

    pub fn load_profiles(&self) -> Result<Vec<ConnectionProfile>> {
        if !self.data_path.exists() {
            return Ok(vec![]);
        }
        let ciphertext = fs::read(&self.data_path).context("leyendo almacenamiento")?;
        let key = self.get_or_create_key()?;
        let plain = match decrypt(&ciphertext, &key) {
            Ok(p) => p,
            Err(_) => {
                self.backup_unreadable_store()?;
                return Ok(vec![]);
            }
        };
        let profiles = serde_json::from_slice(&plain).context("parseando perfiles")?;
        Ok(profiles)
    }

    pub fn save_profiles(&self, profiles: &[ConnectionProfile]) -> Result<()> {
        let key = self.get_or_create_key()?;
        let plain = serde_json::to_vec_pretty(profiles).context("serializando perfiles")?;
        let encrypted = encrypt(&plain, &key).context("cifrando perfiles")?;
        fs::write(&self.data_path, encrypted).context("guardando perfiles")?;
        set_private_file_permissions(&self.data_path).context("asegurando permisos del almacén")?;
        Ok(())
    }

    fn get_or_create_key(&self) -> Result<[u8; 32]> {
        let entry = Entry::new(KEYRING_SERVICE, KEYRING_USER).context("abriendo keychain")?;
        match entry.get_password() {
            Ok(value) => match decode_key(&value) {
                Ok(key) => Ok(key),
                Err(_) => {
                    let has_data = self
                        .data_path
                        .metadata()
                        .map(|m| m.len() > 0)
                        .unwrap_or(false);
                    if has_data {
                        bail!(
                            "clave keychain inválida y hay datos cifrados existentes en {}. \
borra la entrada de keychain del servicio '{}' usuario '{}' para regenerar, \
o respalda y elimina el archivo para iniciar limpio",
                            self.data_path.display(),
                            KEYRING_SERVICE,
                            KEYRING_USER
                        );
                    }
                    let mut key = [0u8; 32];
                    rand::rngs::OsRng.fill_bytes(&mut key);
                    let encoded = B64.encode(key);
                    entry
                        .set_password(&encoded)
                        .context("regenerando clave en keychain")?;
                    Ok(key)
                }
            },
            Err(keyring::Error::NoEntry) => {
                let mut key = [0u8; 32];
                rand::rngs::OsRng.fill_bytes(&mut key);
                let encoded = B64.encode(key);
                entry
                    .set_password(&encoded)
                    .context("guardando clave en keychain")?;
                Ok(key)
            }
            Err(e) => Err(anyhow!("error leyendo keychain: {e}")),
        }
    }

    fn backup_unreadable_store(&self) -> Result<()> {
        if !self.data_path.exists() {
            return Ok(());
        }
        let ts = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|d| d.as_secs())
            .unwrap_or(0);
        let backup_path = self
            .data_path
            .with_extension(format!("enc.unreadable.{ts}.bak"));
        fs::rename(&self.data_path, &backup_path).with_context(|| {
            format!(
                "no se pudo respaldar almacenamiento ilegible ({} -> {})",
                self.data_path.display(),
                backup_path.display()
            )
        })?;
        Ok(())
    }
}

#[cfg(test)]
impl SecureStore {
    /// Test-only constructor: backs the store with an explicit file path so
    /// tests never read from or write to the user's real profiles.
    pub fn with_data_path(data_path: PathBuf) -> Self {
        Self { data_path }
    }
}

/// Envelope of an exported, passphrase-protected backup file.
///
/// The profiles (including passwords) are encrypted with AES-256-GCM using a key
/// derived from the user's passphrase via Argon2id. Only the salt and the
/// ciphertext are stored; the passphrase is never persisted.
#[derive(Debug, Serialize, Deserialize)]
struct BackupEnvelope {
    format: String,
    version: u32,
    kdf: String,
    salt: String,
    data: String,
    #[serde(default)]
    created_at: u64,
    #[serde(default)]
    profiles_count: usize,
}

/// Encrypts the given profiles into a portable, passphrase-protected backup.
pub fn export_backup(profiles: &[ConnectionProfile], passphrase: &str) -> Result<String> {
    if passphrase.is_empty() {
        bail!("la contraseña del backup no puede estar vacía");
    }
    let mut salt = [0u8; BACKUP_SALT_LEN];
    rand::rngs::OsRng.fill_bytes(&mut salt);
    let key = derive_key(passphrase, &salt)?;
    let plain = serde_json::to_vec(profiles).context("serializando perfiles")?;
    let encrypted = encrypt(&plain, &key).context("cifrando backup")?;
    let envelope = BackupEnvelope {
        format: BACKUP_FORMAT.to_string(),
        version: BACKUP_VERSION,
        kdf: "argon2id".to_string(),
        salt: B64.encode(salt),
        data: B64.encode(encrypted),
        created_at: SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|d| d.as_secs())
            .unwrap_or(0),
        profiles_count: profiles.len(),
    };
    serde_json::to_string_pretty(&envelope).context("serializando backup")
}

/// Decrypts a backup produced by [`export_backup`].
pub fn import_backup(json: &str, passphrase: &str) -> Result<Vec<ConnectionProfile>> {
    let envelope: BackupEnvelope =
        serde_json::from_str(json).context("el archivo no es un backup válido de tuissh")?;
    if envelope.format != BACKUP_FORMAT {
        bail!("formato de backup no reconocido: {}", envelope.format);
    }
    if envelope.version != BACKUP_VERSION {
        bail!(
            "versión de backup no soportada: {} (se esperaba {})",
            envelope.version,
            BACKUP_VERSION
        );
    }
    let salt = B64
        .decode(&envelope.salt)
        .context("salt inválido en el backup")?;
    if salt.len() != BACKUP_SALT_LEN {
        bail!("salt inválido en el backup");
    }
    let blob = B64
        .decode(&envelope.data)
        .context("datos cifrados inválidos en el backup")?;
    let key = derive_key(passphrase, &salt)?;
    let plain = decrypt(&blob, &key)
        .map_err(|_| anyhow!("contraseña incorrecta o backup corrupto"))?;
    let profiles = serde_json::from_slice(&plain).context("contenido del backup ilegible")?;
    Ok(profiles)
}

fn derive_key(passphrase: &str, salt: &[u8]) -> Result<[u8; 32]> {
    let argon = Argon2::new(Algorithm::Argon2id, Version::V0x13, Params::default());
    let mut key = [0u8; 32];
    argon
        .hash_password_into(passphrase.as_bytes(), salt, &mut key)
        .map_err(|e| anyhow!("derivando clave del backup: {e}"))?;
    Ok(key)
}

/// Sets the mode of a file to owner-only (`0600`) on Unix.
pub fn set_private_file_permissions(path: &Path) -> Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(path, fs::Permissions::from_mode(0o600))
            .with_context(|| format!("ajustando permisos de {}", path.display()))?;
    }
    let _ = path;
    Ok(())
}

/// Sets the mode of a directory to owner-only (`0700`) on Unix.
pub fn set_private_dir_permissions(path: &Path) -> Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(path, fs::Permissions::from_mode(0o700))
            .with_context(|| format!("ajustando permisos de {}", path.display()))?;
    }
    let _ = path;
    Ok(())
}

fn decode_key(encoded: &str) -> Result<[u8; 32]> {
    let trimmed = encoded.trim();

    if let Ok(key) = decode_key_candidate(trimmed) {
        return Ok(key);
    }

    if let Ok(value) = serde_json::from_str::<serde_json::Value>(trimmed) {
        if let Some(key) = decode_key_from_json(&value) {
            return Ok(key);
        }
    }

    for token in split_legacy_tokens(trimmed) {
        if let Ok(key) = decode_key_candidate(token) {
            return Ok(key);
        }
    }

    Err(anyhow!("clave keychain base64 inválida"))
}

fn decode_key_from_json(value: &serde_json::Value) -> Option<[u8; 32]> {
    let keys = ["key", "value", "secret", "password", "data"];
    if let Some(obj) = value.as_object() {
        for k in keys {
            if let Some(v) = obj.get(k).and_then(|v| v.as_str()) {
                if let Ok(key) = decode_key_candidate(v.trim()) {
                    return Some(key);
                }
            }
        }
    }
    value
        .as_str()
        .and_then(|s| decode_key_candidate(s.trim()).ok())
}

fn split_legacy_tokens(s: &str) -> Vec<&str> {
    s.split(|c: char| c == ':' || c == ';' || c.is_whitespace())
        .filter(|t| !t.is_empty())
        .collect()
}

fn decode_key_candidate(s: &str) -> Result<[u8; 32]> {
    if let Ok(raw) = B64.decode(s) {
        return to_key_array(&raw);
    }
    if let Ok(raw) = URL_SAFE.decode(s) {
        return to_key_array(&raw);
    }
    if let Ok(raw) = URL_SAFE_NO_PAD.decode(s) {
        return to_key_array(&raw);
    }
    if let Ok(raw) = decode_hex_32(s) {
        return to_key_array(&raw);
    }
    if s.as_bytes().len() == 32 {
        return to_key_array(s.as_bytes());
    }
    bail!("invalid key candidate")
}

fn to_key_array(raw: &[u8]) -> Result<[u8; 32]> {
    if raw.len() != 32 {
        bail!("invalid key size");
    }
    let mut out = [0u8; 32];
    out.copy_from_slice(raw);
    Ok(out)
}

fn decode_hex_32(s: &str) -> Result<Vec<u8>> {
    let s = s.strip_prefix("0x").unwrap_or(s);
    if s.len() != 64 || !s.as_bytes().iter().all(|b| b.is_ascii_hexdigit()) {
        bail!("not hex");
    }
    let mut out = Vec::with_capacity(32);
    let bytes = s.as_bytes();
    let mut i = 0;
    while i < bytes.len() {
        let hi = hex_val(bytes[i])?;
        let lo = hex_val(bytes[i + 1])?;
        out.push((hi << 4) | lo);
        i += 2;
    }
    Ok(out)
}

fn hex_val(b: u8) -> Result<u8> {
    match b {
        b'0'..=b'9' => Ok(b - b'0'),
        b'a'..=b'f' => Ok(10 + b - b'a'),
        b'A'..=b'F' => Ok(10 + b - b'A'),
        _ => bail!("invalid hex"),
    }
}

fn encrypt(data: &[u8], key: &[u8; 32]) -> Result<Vec<u8>> {
    let cipher = Aes256Gcm::new_from_slice(key).expect("key size fixed");
    let mut nonce = [0u8; 12];
    rand::rngs::OsRng.fill_bytes(&mut nonce);
    let encrypted = cipher
        .encrypt(Nonce::from_slice(&nonce), data)
        .map_err(|_| anyhow!("falló cifrado"))?;
    let mut out = Vec::with_capacity(12 + encrypted.len());
    out.extend_from_slice(&nonce);
    out.extend_from_slice(&encrypted);
    Ok(out)
}

fn decrypt(data: &[u8], key: &[u8; 32]) -> Result<Vec<u8>> {
    if data.len() < 12 {
        bail!("contenido cifrado inválido");
    }
    let (nonce, ciphertext) = data.split_at(12);
    let cipher = Aes256Gcm::new_from_slice(key).expect("key size fixed");
    cipher
        .decrypt(Nonce::from_slice(nonce), ciphertext)
        .map_err(|_| anyhow!("falló descifrado"))
}

#[cfg(test)]
mod tests {
    use super::*;


    #[test]
    fn roundtrip_encryption() {
        let key = [7u8; 32];
        let data = b"hola mundo";
        let enc = encrypt(data, &key).unwrap();
        assert_eq!(decrypt(&enc, &key).unwrap(), data);
    }

    #[test]
    fn decrypt_with_wrong_key_fails() {
        let enc = encrypt(b"secret", &[1u8; 32]).unwrap();
        assert!(decrypt(&enc, &[2u8; 32]).is_err());
    }

    #[test]
    fn rejects_truncated_ciphertext() {
        assert!(decrypt(&[0u8; 4], &[0u8; 32]).is_err());
    }

    #[test]
    fn decodes_base64_key() {
        let raw = [9u8; 32];
        let encoded = B64.encode(raw);
        assert_eq!(decode_key(&encoded).unwrap(), raw);
    }

    fn sample() -> Vec<ConnectionProfile> {
        vec![ConnectionProfile {
            name: "prod".into(),
            host: "example.com".into(),
            port: 2222,
            user: "root".into(),
            password: "s3cr3t".into(),
            ..Default::default()
        }]
    }

    #[test]
    fn backup_roundtrip_preserves_passwords() {
        let json = export_backup(&sample(), "correct horse").unwrap();
        let restored = import_backup(&json, "correct horse").unwrap();
        assert_eq!(restored.len(), 1);
        assert_eq!(restored[0].host, "example.com");
        assert_eq!(restored[0].port, 2222);
        assert_eq!(restored[0].password, "s3cr3t");
    }

    #[test]
    fn backup_wrong_passphrase_is_rejected() {
        let json = export_backup(&sample(), "correct").unwrap();
        assert!(import_backup(&json, "incorrect").is_err());
    }

    #[test]
    fn backup_rejects_empty_passphrase() {
        assert!(export_backup(&sample(), "").is_err());
    }

    #[test]
    fn backup_rejects_foreign_or_corrupt_files() {
        assert!(import_backup("not json at all", "x").is_err());
        assert!(import_backup("{\"hola\":1}", "x").is_err());
    }

    #[test]
    fn backup_ciphertext_is_not_plaintext() {
        let json = export_backup(&sample(), "pw").unwrap();
        assert!(!json.contains("s3cr3t"), "el backup filtró el password");
        assert!(!json.contains("example.com"), "el backup filtró el host");
    }

    #[test]
    fn backup_file_roundtrip_via_disk() {
        let dir = std::env::temp_dir().join(format!("tuissh-backup-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("backup.json");

        let json = export_backup(&sample(), "pw").unwrap();
        std::fs::write(&path, json).unwrap();
        set_private_file_permissions(&path).unwrap();

        let content = std::fs::read_to_string(&path).unwrap();
        let restored = import_backup(&content, "pw").unwrap();
        assert_eq!(restored.len(), 1);
        assert_eq!(restored[0].password, "s3cr3t");

        let _ = std::fs::remove_dir_all(&dir);
    }
}
