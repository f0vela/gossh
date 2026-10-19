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
pub const BACKUP_VERSION: u32 = 2;
/// Oldest backup version we can still import (v1 used fixed, weaker KDF costs).
const BACKUP_MIN_VERSION: u32 = 1;
/// Minimum passphrase length (in characters) required to create a backup. It
/// protects every stored password offline, so a short one is a brute-force risk.
pub const MIN_BACKUP_PASSPHRASE_CHARS: usize = 12;
/// Size of the random salt used for the passphrase KDF.
const BACKUP_SALT_LEN: usize = 16;

/// Argon2id cost parameters, stored in the backup so they can be raised later.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct KdfParams {
    m_cost: u32,
    t_cost: u32,
    p_cost: u32,
}

/// Costs used for new backups: 64 MiB, 3 passes.
const BACKUP_KDF: KdfParams = KdfParams {
    m_cost: 64 * 1024,
    t_cost: 3,
    p_cost: 1,
};

/// Costs v1 backups were written with (the `argon2` crate defaults).
const LEGACY_KDF: KdfParams = KdfParams {
    m_cost: 19 * 1024,
    t_cost: 2,
    p_cost: 1,
};

/// Upper bounds accepted from a backup file, so a crafted one cannot make the
/// importer allocate gigabytes or spin for minutes.
const MAX_KDF_M_COST: u32 = 512 * 1024;
const MAX_KDF_T_COST: u32 = 10;
const MAX_KDF_P_COST: u32 = 8;

/// Result of loading the profile store.
pub struct LoadedProfiles {
    pub profiles: Vec<ConnectionProfile>,
    /// Set when the store existed but could not be decrypted and was moved
    /// aside; the user must be told, otherwise it looks like profiles vanished.
    pub notice: Option<String>,
}

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

    pub fn load_profiles(&self) -> Result<LoadedProfiles> {
        if !self.data_path.exists() {
            return Ok(LoadedProfiles {
                profiles: vec![],
                notice: None,
            });
        }
        let ciphertext = fs::read(&self.data_path).context("leyendo almacenamiento")?;
        let key = self.get_or_create_key()?;
        let plain = match decrypt(&ciphertext, &key) {
            Ok(p) => p,
            Err(_) => {
                let moved = self.backup_unreadable_store()?;
                let notice = format!(
                    "No se pudieron descifrar tus perfiles guardados (¿cambió o se perdió la \
clave del keychain '{KEYRING_SERVICE}'?). El archivo NO se borró: quedó en {}. \
Si recuperas la clave, restaura ese archivo como {}; si tienes un backup, impórtalo con Ctrl+Q r.",
                    moved.display(),
                    self.data_path.display()
                );
                return Ok(LoadedProfiles {
                    profiles: vec![],
                    notice: Some(notice),
                });
            }
        };
        let profiles = serde_json::from_slice(&plain).context("parseando perfiles")?;
        Ok(LoadedProfiles {
            profiles,
            notice: None,
        })
    }

    pub fn save_profiles(&self, profiles: &[ConnectionProfile]) -> Result<()> {
        let key = self.get_or_create_key()?;
        let plain = serde_json::to_vec_pretty(profiles).context("serializando perfiles")?;
        let encrypted = encrypt(&plain, &key).context("cifrando perfiles")?;
        // Write to a sibling temp file and rename, so a crash mid-write can
        // never leave a truncated store (which would be unreadable).
        let tmp = self.data_path.with_extension("enc.tmp");
        write_private(&tmp, &encrypted).context("guardando perfiles")?;
        fs::rename(&tmp, &self.data_path).context("reemplazando almacén")?;
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
                    store_new_key(&entry).context("regenerando clave en keychain")

                }
            },
            Err(keyring::Error::NoEntry) => {
                store_new_key(&entry).context("guardando clave en keychain")
            }
            Err(e) => Err(anyhow!("error leyendo keychain: {e}")),
        }
    }

    /// Moves the unreadable store aside and returns where it went.
    fn backup_unreadable_store(&self) -> Result<PathBuf> {
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
        Ok(backup_path)
    }
}

/// Generates a random master key and saves it in the keychain entry.
fn store_new_key(entry: &Entry) -> Result<[u8; 32]> {
    let mut key = [0u8; 32];
    rand::rngs::OsRng.fill_bytes(&mut key);
    entry.set_password(&B64.encode(key))?;
    Ok(key)
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
    /// Argon2id costs (v2+). Absent in v1 files, which used `LEGACY_KDF`.
    #[serde(default)]
    m_cost: u32,
    #[serde(default)]
    t_cost: u32,
    #[serde(default)]
    p_cost: u32,
    #[serde(default)]
    created_at: u64,
    #[serde(default)]
    profiles_count: usize,
}

/// Encrypts the given profiles into a portable, passphrase-protected backup.
pub fn export_backup(profiles: &[ConnectionProfile], passphrase: &str) -> Result<String> {
    if passphrase.chars().count() < MIN_BACKUP_PASSPHRASE_CHARS {
        bail!(
            "la contraseña del backup debe tener al menos {MIN_BACKUP_PASSPHRASE_CHARS} caracteres"
        );
    }
    export_backup_with(profiles, passphrase, BACKUP_KDF)
}

fn export_backup_with(
    profiles: &[ConnectionProfile],
    passphrase: &str,
    kdf: KdfParams,
) -> Result<String> {
    let mut salt = [0u8; BACKUP_SALT_LEN];
    rand::rngs::OsRng.fill_bytes(&mut salt);
    let key = derive_key(passphrase, &salt, kdf)?;
    let plain = serde_json::to_vec(profiles).context("serializando perfiles")?;
    let encrypted = encrypt(&plain, &key).context("cifrando backup")?;
    let envelope = BackupEnvelope {
        format: BACKUP_FORMAT.to_string(),
        version: BACKUP_VERSION,
        kdf: "argon2id".to_string(),
        salt: B64.encode(salt),
        data: B64.encode(encrypted),
        m_cost: kdf.m_cost,
        t_cost: kdf.t_cost,
        p_cost: kdf.p_cost,
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
    if !(BACKUP_MIN_VERSION..=BACKUP_VERSION).contains(&envelope.version) {
        bail!(
            "versión de backup no soportada: {} (se soportan {}..={})",
            envelope.version,
            BACKUP_MIN_VERSION,
            BACKUP_VERSION
        );
    }
    let kdf = if envelope.version == 1 {
        LEGACY_KDF
    } else {
        let kdf = KdfParams {
            m_cost: envelope.m_cost,
            t_cost: envelope.t_cost,
            p_cost: envelope.p_cost,
        };
        if kdf.m_cost == 0
            || kdf.t_cost == 0
            || kdf.p_cost == 0
            || kdf.m_cost > MAX_KDF_M_COST
            || kdf.t_cost > MAX_KDF_T_COST
            || kdf.p_cost > MAX_KDF_P_COST
        {
            bail!("parámetros de derivación de clave fuera de rango en el backup");
        }
        kdf
    };
    let salt = B64
        .decode(&envelope.salt)
        .context("salt inválido en el backup")?;
    if salt.len() != BACKUP_SALT_LEN {
        bail!("salt inválido en el backup");
    }
    let blob = B64
        .decode(&envelope.data)
        .context("datos cifrados inválidos en el backup")?;
    let key = derive_key(passphrase, &salt, kdf)?;
    let plain = decrypt(&blob, &key)
        .map_err(|_| anyhow!("contraseña incorrecta o backup corrupto"))?;
    let profiles = serde_json::from_slice(&plain).context("contenido del backup ilegible")?;
    Ok(profiles)
}

fn derive_key(passphrase: &str, salt: &[u8], kdf: KdfParams) -> Result<[u8; 32]> {
    let params = Params::new(kdf.m_cost, kdf.t_cost, kdf.p_cost, Some(32))
        .map_err(|e| anyhow!("parámetros Argon2 inválidos: {e}"))?;
    let argon = Argon2::new(Algorithm::Argon2id, Version::V0x13, params);
    let mut key = [0u8; 32];
    argon
        .hash_password_into(passphrase.as_bytes(), salt, &mut key)
        .map_err(|e| anyhow!("derivando clave del backup: {e}"))?;
    Ok(key)
}

/// Writes `data` to `path`, creating the file owner-only (`0600`) from the
/// start. `fs::write` followed by `chmod` leaves a window where the file is
/// readable under the default umask.
pub fn write_private(path: &Path, data: &[u8]) -> Result<()> {
    use std::io::Write;
    let mut opts = fs::OpenOptions::new();
    opts.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o600);
    }
    let mut file = opts
        .open(path)
        .with_context(|| format!("abriendo {}", path.display()))?;
    file.write_all(data)
        .with_context(|| format!("escribiendo {}", path.display()))?;
    // A pre-existing file keeps its old mode; tighten it too.
    set_private_file_permissions(path)
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
        let json = export_backup(&sample(), "correct-passphrase").unwrap();
        assert!(import_backup(&json, "incorrect-passphrase").is_err());
    }

    #[test]
    fn backup_rejects_empty_and_short_passphrases() {
        assert!(export_backup(&sample(), "").is_err());
        assert!(export_backup(&sample(), "short").is_err());
        assert!(export_backup(&sample(), "elevenchars").is_err());
        assert!(export_backup(&sample(), "twelve chars").is_ok());
    }

    #[test]
    fn backup_records_kdf_costs_and_version() {
        let json = export_backup(&sample(), "correct horse").unwrap();
        let v: serde_json::Value = serde_json::from_str(&json).unwrap();
        assert_eq!(v["version"], BACKUP_VERSION);
        assert_eq!(v["m_cost"], BACKUP_KDF.m_cost);
        assert_eq!(v["t_cost"], BACKUP_KDF.t_cost);
    }

    #[test]
    fn legacy_v1_backup_still_imports() {
        // Build a v1 file: legacy costs, no cost fields, any passphrase length.
        let json = export_backup_with(&sample(), "pw", LEGACY_KDF).unwrap();
        let mut v: serde_json::Value = serde_json::from_str(&json).unwrap();
        v["version"] = 1.into();
        for k in ["m_cost", "t_cost", "p_cost"] {
            v.as_object_mut().unwrap().remove(k);
        }
        let restored = import_backup(&v.to_string(), "pw").unwrap();
        assert_eq!(restored[0].password, "s3cr3t");
    }

    #[test]
    fn backup_rejects_out_of_range_kdf_costs() {
        let json = export_backup(&sample(), "correct horse").unwrap();
        let mut v: serde_json::Value = serde_json::from_str(&json).unwrap();
        v["m_cost"] = (MAX_KDF_M_COST + 1).into();
        assert!(import_backup(&v.to_string(), "correct horse").is_err());
        v["m_cost"] = 0.into();
        assert!(import_backup(&v.to_string(), "correct horse").is_err());
    }

    #[cfg(unix)]
    #[test]
    fn write_private_creates_owner_only_file() {
        use std::os::unix::fs::PermissionsExt;
        let dir = std::env::temp_dir().join(format!("tuissh-wp-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("f");
        write_private(&path, b"x").unwrap();
        assert_eq!(std::fs::metadata(&path).unwrap().permissions().mode() & 0o777, 0o600);
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn unreadable_store_is_moved_aside_with_a_notice() {
        let dir = std::env::temp_dir().join(format!("tuissh-unread-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("profiles.enc");
        std::fs::write(&path, b"this is not a valid ciphertext at all").unwrap();
        let store = SecureStore::with_data_path(path.clone());
        let moved = store.backup_unreadable_store().unwrap();
        assert!(moved.exists());
        assert!(!path.exists());
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn backup_rejects_foreign_or_corrupt_files() {
        assert!(import_backup("not json at all", "x").is_err());
        assert!(import_backup("{\"hola\":1}", "x").is_err());
    }

    #[test]
    fn backup_ciphertext_is_not_plaintext() {
        let json = export_backup(&sample(), "pw-long-enough-1").unwrap();
        assert!(!json.contains("s3cr3t"), "el backup filtró el password");
        assert!(!json.contains("example.com"), "el backup filtró el host");
    }

    #[test]
    fn backup_file_roundtrip_via_disk() {
        let dir = std::env::temp_dir().join(format!("tuissh-backup-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("backup.json");

        let json = export_backup(&sample(), "pw-long-enough-1").unwrap();
        std::fs::write(&path, json).unwrap();
        set_private_file_permissions(&path).unwrap();

        let content = std::fs::read_to_string(&path).unwrap();
        let restored = import_backup(&content, "pw-long-enough-1").unwrap();
        assert_eq!(restored.len(), 1);
        assert_eq!(restored[0].password, "s3cr3t");

        let _ = std::fs::remove_dir_all(&dir);
    }
}
