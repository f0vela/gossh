use std::fs;
use std::io::{self, Read, Write};
use std::net::TcpStream;
use std::path::{Path, PathBuf};
use std::sync::mpsc::{Receiver, Sender, TryRecvError};
use std::thread::{self, JoinHandle};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use anyhow::{anyhow, bail, Context, Result};
use base64::{
    engine::general_purpose::{STANDARD as B64, URL_SAFE_NO_PAD},
    Engine as _,
};
use ssh2::{
    Channel, CheckResult, HashType, KeyboardInteractivePrompt, KnownHostFileKind, PtyModeOpcode,
    PtyModes, Prompt, Session,
};

use crate::store::{AuthMethod, ConnectionProfile};

/// Appends a timestamped diagnostic line to the file named by `$TUISSH_LOG`,
/// when that variable is set. Used to debug session drops without touching
/// normal output.
pub(crate) fn debug_log(msg: &str) {
    let Ok(path) = std::env::var("TUISSH_LOG") else {
        return;
    };
    let ts = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis())
        .unwrap_or(0);
    if let Ok(mut f) = fs::OpenOptions::new().create(true).append(true).open(&path) {
        let _ = writeln!(f, "{ts} {msg}");
    }
}

/// Result of a non-blocking read from the interactive shell channel.
enum ShellPoll {
    Data(Vec<u8>),
    Idle,
    Eof,
}

pub struct SSHClient {
    session: Session,
    sftp: ssh2::Sftp,
    home: String,
    shell: Option<Channel>,
    /// Duplicate handle of the TCP socket, used only to detect that the peer
    /// closed the connection. libssh2 can keep reporting an idle read long
    /// after the link is gone, which is why keystrokes used to be swallowed.
    probe: TcpStream,
    cols: u16,
    rows: u16,
}

/// Maximum bytes of typed input we buffer while the channel is momentarily
/// unable to accept more. Reaching this means the remote is not reading us.
const MAX_PENDING_INPUT: usize = 64 * 1024;

impl SSHClient {
    /// Connects, authenticates, verifies the host key, initializes SFTP and
    /// opens an interactive PTY shell sized `cols` x `rows`.
    pub fn connect(profile: &ConnectionProfile, cols: u16, rows: u16) -> Result<Self> {
        let tcp = TcpStream::connect(format!("{}:{}", profile.host, profile.port))
            .with_context(|| format!("conectando a {}:{}", profile.host, profile.port))?;
        tcp.set_read_timeout(Some(Duration::from_secs(20))).ok();
        tcp.set_write_timeout(Some(Duration::from_secs(20))).ok();
        // A second handle to the same socket lets us notice a FIN/RST that
        // libssh2 has not surfaced yet.
        let probe = tcp.try_clone().context("duplicando socket TCP")?;

        let mut session = Session::new().context("creando sesión SSH")?;
        session.set_tcp_stream(tcp);
        session.handshake().context("handshake SSH")?;

        verify_host_key(&session, profile)?;
        authenticate(&session, profile)?;

        if !session.authenticated() {
            bail!("autenticación SSH no completada");
        }

        let sftp = session.sftp().context("inicializando SFTP")?;
        let home = sftp
            .realpath(Path::new("."))
            .map(|p| p.to_string_lossy().to_string())
            .unwrap_or_else(|_| "/".to_string());

        let mut client = Self {
            session,
            sftp,
            home,
            shell: None,
            probe,
            cols,
            rows,
        };
        client.start_shell(cols, rows)?;
        // From here on the client is used in non-blocking mode so the worker can
        // poll the shell while staying responsive.
        client.session.set_blocking(false);
        // Keepalives keep a silent link from being dropped by the server or an
        // intermediate NAT/firewall, and they flush libssh2's queued packets.
        client.session.set_keepalive(true, 20);
        Ok(client)
    }

    pub fn home(&self) -> &str {
        &self.home
    }

    fn start_shell(&mut self, cols: u16, rows: u16) -> Result<()> {
        let mut channel = self
            .session
            .channel_session()
            .context("creando canal de shell")?;
        let dim = Some((cols as u32, rows as u32, 0, 0));
        // Disable software flow control (XON/XOFF) on the remote PTY. Otherwise a
        // stray Ctrl+S suspends all remote output and the session *looks* frozen
        // while you are still connected (and Ctrl+Q, the XON key, is our command
        // prefix). If the server rejects the modes, fall back to the defaults.
        let mut modes = PtyModes::new();
        modes.set_boolean(PtyModeOpcode::IXON, false);
        modes.set_boolean(PtyModeOpcode::IXANY, false);
        modes.set_boolean(PtyModeOpcode::IXOFF, false);
        if channel
            .request_pty("xterm-256color", Some(modes), dim)
            .is_err()
        {
            channel
                .request_pty("xterm-256color", None, dim)
                .context("solicitando PTY")?;
        }
        channel.shell().context("iniciando shell remota")?;
        self.shell = Some(channel);
        self.cols = cols;
        self.rows = rows;
        Ok(())
    }

    fn poll_shell(&mut self) -> Result<ShellPoll> {
        let Some(channel) = self.shell.as_mut() else {
            return Ok(ShellPoll::Eof);
        };
        let mut buf = [0u8; 65536];
        match channel.read(&mut buf) {
            Ok(0) => {
                if channel.eof() {
                    debug_log("poll_shell: EOF del canal remoto");
                    Ok(ShellPoll::Eof)
                } else {
                    Ok(ShellPoll::Idle)
                }
            }
            Ok(n) => Ok(ShellPoll::Data(buf[..n].to_vec())),
            Err(e) if e.kind() == io::ErrorKind::WouldBlock => Ok(ShellPoll::Idle),
            Err(e) => {
                debug_log(&format!("poll_shell: error de lectura: {e}"));
                Err(anyhow!("leyendo shell: {e}"))
            }
        }
    }

    /// Writes as much of `bytes` as the channel accepts without blocking.
    ///
    /// Returns the number of bytes accepted; `0` means the channel is
    /// momentarily full, so the caller must retry *after* pumping reads (that
    /// is what lets libssh2 process the window updates that unblock us).
    ///
    /// Never calls `Channel::flush()`: in libssh2 that discards buffered
    /// *incoming* channel data, which would silently eat the remote echo.
    fn try_write(&mut self, bytes: &[u8]) -> Result<usize> {
        let Some(channel) = self.shell.as_mut() else {
            return Ok(bytes.len());
        };
        match channel.write(bytes) {
            Ok(n) => Ok(n),
            Err(e) if e.kind() == io::ErrorKind::WouldBlock => Ok(0),
            Err(e) => {
                debug_log(&format!("try_write: error duro: {e}"));
                Err(anyhow!("escribiendo en el shell: {e}"))
            }
        }
    }

    /// Sends a keepalive when one is due, which also flushes any packet
    /// libssh2 has queued. Called periodically by the worker even while the
    /// user is idle.
    fn keepalive(&mut self) {
        let _ = self.session.keepalive_send();
    }

    /// True when the peer has closed the TCP connection. libssh2 can report an
    /// idle read for a socket that is already gone, so we peek at the raw
    /// connection to notice it before the user types again.
    fn peer_closed(&self) -> bool {
        let mut byte = [0u8; 1];
        match self.probe.peek(&mut byte) {
            Ok(0) => true,
            Ok(_) => false,
            Err(ref e) if e.kind() == io::ErrorKind::WouldBlock => false,
            Err(_) => true,
        }
    }

    fn resize(&mut self, cols: u16, rows: u16) -> Result<()> {
        if self.cols == cols && self.rows == rows {
            return Ok(());
        }
        if let Some(channel) = self.shell.as_mut() {
            self.session.set_blocking(true);
            let r = channel.request_pty_size(cols as u32, rows as u32, None, None);
            self.session.set_blocking(false);
            r.context("cambiando tamaño del PTY")?;
        }
        self.cols = cols;
        self.rows = rows;
        Ok(())
    }

    fn close_shell(&mut self) {
        if let Some(mut channel) = self.shell.take() {
            self.session.set_blocking(true);
            let _ = channel.send_eof();
            let _ = channel.close();
            self.session.set_blocking(false);
        }
    }

    pub fn upload_file(&mut self, local: &str, remote: &str) -> Result<()> {
        self.session.set_blocking(true);
        let result = self.upload_file_inner(local, remote);
        self.session.set_blocking(false);
        result
    }

    fn upload_file_inner(&mut self, local: &str, remote: &str) -> Result<()> {
        let data = fs::read(local).with_context(|| format!("leyendo archivo local {}", local))?;
        let remote_path = self.resolve_remote_path(remote);
        self.ensure_remote_parent(&remote_path)?;
        let mut f = self
            .sftp
            .create(Path::new(&remote_path))
            .with_context(|| format!("creando remoto {remote_path}"))?;
        f.write_all(&data).context("escribiendo archivo remoto")?;
        Ok(())
    }

    pub fn read_remote_file(&mut self, remote: &str) -> Result<String> {
        self.session.set_blocking(true);
        let result = self.read_remote_file_inner(remote);
        self.session.set_blocking(false);
        result
    }

    fn read_remote_file_inner(&mut self, remote: &str) -> Result<String> {
        let remote_path = self.resolve_remote_path(remote);
        let mut f = self
            .sftp
            .open(Path::new(&remote_path))
            .with_context(|| format!("abriendo remoto {remote_path}"))?;
        let mut out = String::new();
        f.read_to_string(&mut out)
            .context("leyendo archivo remoto")?;
        Ok(out)
    }

    pub fn write_remote_file(&mut self, remote: &str, content: &str) -> Result<()> {
        self.session.set_blocking(true);
        let result = self.write_remote_file_inner(remote, content);
        self.session.set_blocking(false);
        result
    }

    fn write_remote_file_inner(&mut self, remote: &str, content: &str) -> Result<()> {
        let remote_path = self.resolve_remote_path(remote);
        self.ensure_remote_parent(&remote_path)?;
        let mut f = self
            .sftp
            .create(Path::new(&remote_path))
            .with_context(|| format!("creando remoto {remote_path}"))?;
        f.write_all(content.as_bytes())
            .context("escribiendo contenido remoto")?;
        Ok(())
    }

    fn resolve_remote_path(&self, p: &str) -> String {
        let p = p.trim();
        if p.is_empty() || p == "." {
            return self.home.clone();
        }
        if p == "~" {
            return self.home.clone();
        }
        if let Some(rest) = p.strip_prefix("~/") {
            return normalize_posix_path(&format!("{}/{}", self.home, rest));
        }
        if p.starts_with('/') {
            return normalize_posix_path(p);
        }
        normalize_posix_path(&format!("{}/{}", self.home, p))
    }

    fn ensure_remote_parent(&mut self, remote_path: &str) -> Result<()> {
        let parent = remote_path.rsplit_once('/').map(|v| v.0).unwrap_or("/");
        self.mkdir_all(parent)
    }

    fn mkdir_all(&mut self, dir: &str) -> Result<()> {
        if dir.is_empty() || dir == "/" {
            return Ok(());
        }
        let cleaned = normalize_posix_path(dir);
        let mut cur = String::from("/");
        for part in cleaned.split('/').filter(|p| !p.is_empty()) {
            if cur != "/" {
                cur.push('/');
            }
            cur.push_str(part);
            if self.sftp.stat(Path::new(&cur)).is_err() {
                let _ = self.sftp.mkdir(Path::new(&cur), 0o755);
            }
        }
        Ok(())
    }
}

/// Attempts authentication methods in a sensible order for the profile's
/// configured method, falling back across methods until one succeeds.
fn authenticate(session: &Session, profile: &ConnectionProfile) -> Result<()> {
    let has_password = !profile.password.trim().is_empty();
    let has_key = !profile.private_key_path.trim().is_empty();

    let attempts: Vec<AuthMethod> = match profile.auth_method {
        AuthMethod::Password => vec![AuthMethod::Password, AuthMethod::Interactive],
        AuthMethod::Interactive => vec![AuthMethod::Interactive, AuthMethod::Password],
        AuthMethod::Key => vec![AuthMethod::Key],
        AuthMethod::Both => vec![
            AuthMethod::Key,
            AuthMethod::Password,
            AuthMethod::Interactive,
        ],
    };

    let mut last_err: Option<String> = None;
    for method in attempts {
        let result = match method {
            AuthMethod::Password => {
                if has_password {
                    session
                        .userauth_password(&profile.user, &profile.password)
                        .map_err(|e| anyhow!("password: {e}"))
                } else {
                    Err(anyhow!("password: sin password configurado"))
                }
            }
            AuthMethod::Interactive => {
                if has_password {
                    auth_keyboard_interactive(session, profile)
                } else {
                    Err(anyhow!("keyboard-interactive: sin password configurado"))
                }
            }
            AuthMethod::Key => {
                if has_key {
                    auth_key(session, profile)
                } else {
                    Err(anyhow!("key: sin private key configurada"))
                }
            }
            AuthMethod::Both => unreachable!("Both se expande en la lista de intentos"),
        };

        match result {
            Ok(()) if session.authenticated() => return Ok(()),
            Ok(()) => {}
            Err(e) => last_err = Some(e.to_string()),
        }
    }

    Err(anyhow!(
        "autenticación SSH falló: {}",
        last_err.unwrap_or_else(|| "sin métodos aplicables".to_string())
    ))
}

fn auth_key(session: &Session, profile: &ConnectionProfile) -> Result<()> {
    if profile.private_key_path.trim().is_empty() {
        bail!("private key path requerido para auth=key");
    }
    let path = expand_tilde(&profile.private_key_path)?;
    let passphrase = if profile.private_key_pass.trim().is_empty() {
        None
    } else {
        Some(profile.private_key_pass.as_str())
    };
    session
        .userauth_pubkey_file(&profile.user, None, Path::new(&path), passphrase)
        .with_context(|| format!("autenticación key falló ({})", profile.private_key_path))?;
    Ok(())
}

/// Responses to keyboard-interactive challenges using the stored password.
struct PasswordPrompter {
    password: String,
}

impl KeyboardInteractivePrompt for PasswordPrompter {
    fn prompt<'a>(&mut self, _u: &str, _i: &str, prompts: &[Prompt<'a>]) -> Vec<String> {
        prompts
            .iter()
            .map(|p| {
                if p.echo {
                    String::new()
                } else {
                    self.password.clone()
                }
            })
            .collect()
    }
}

fn auth_keyboard_interactive(session: &Session, profile: &ConnectionProfile) -> Result<()> {
    let mut prompter = PasswordPrompter {
        password: profile.password.clone(),
    };
    session
        .userauth_keyboard_interactive(&profile.user, &mut prompter)
        .context("autenticación keyboard-interactive falló")?;
    Ok(())
}

fn expand_tilde(p: &str) -> Result<String> {
    if let Some(rest) = p.strip_prefix("~/") {
        let home = dirs::home_dir().ok_or_else(|| anyhow!("no se pudo detectar home"))?;
        return Ok(home.join(rest).to_string_lossy().to_string());
    }
    Ok(p.to_string())
}

fn normalize_posix_path(p: &str) -> String {
    let mut parts = Vec::new();
    let absolute = p.starts_with('/');
    for part in p.split('/') {
        match part {
            "" | "." => {}
            ".." => {
                parts.pop();
            }
            other => parts.push(other),
        }
    }
    let joined = parts.join("/");
    if absolute {
        if joined.is_empty() {
            "/".to_string()
        } else {
            format!("/{joined}")
        }
    } else if joined.is_empty() {
        ".".to_string()
    } else {
        joined
    }
}

/// Path to the user's OpenSSH known_hosts file.
fn known_hosts_path() -> Result<PathBuf> {
    let home = dirs::home_dir().ok_or_else(|| anyhow!("no se pudo detectar home"))?;
    Ok(home.join(".ssh").join("known_hosts"))
}

/// Verifies the remote host key against `~/.ssh/known_hosts`.
///
/// - Match: connection is trusted.
/// - Mismatch: hard failure (possible MITM, or the server was reinstalled).
/// - NotFound: trust-on-first-use; the key is appended to known_hosts.
fn verify_host_key(session: &Session, profile: &ConnectionProfile) -> Result<()> {
    let Some((key, key_type)) = session.host_key() else {
        bail!("el servidor no entregó una host key; conexión abortada");
    };

    let path = known_hosts_path()?;
    let mut known_hosts = session.known_hosts().context("inicializando known_hosts")?;
    if path.exists() {
        // Ignore parse errors from unrelated/legacy lines.
        let _ = known_hosts.read_file(&path, KnownHostFileKind::OpenSSH);
    }

    match known_hosts.check_port(&profile.host, profile.port, key) {
        CheckResult::Match => Ok(()),
        CheckResult::Mismatch => {
            let fp = sha256_fingerprint(session).unwrap_or_default();
            bail!(
                "¡LA HOST KEY CAMBIÓ para {}:{}!\n\
                 Esto puede ser un ataque man-in-the-middle o que el servidor fue reinstalado.\n\
                 Huella recibida: {fp}\n\
                 Si confías en el cambio, elimina la entrada de {} para {} y reintenta.",
                profile.host,
                profile.port,
                path.display(),
                profile.host
            );
        }
        CheckResult::Failure => {
            bail!("no se pudo verificar la host key de {}", profile.host)
        }
        CheckResult::NotFound => {
            if matches!(key_type, ssh2::HostKeyType::Unknown) {
                bail!(
                    "tipo de host key desconocido para {}; se rechaza por seguridad",
                    profile.host
                );
            }
            let line = format!(
                "{} {} {}\n",
                known_hosts_host_field(&profile.host, profile.port),
                host_key_type_name(key_type),
                B64.encode(key)
            );
            append_known_host(&path, &line)
                .with_context(|| format!("guardando host key en {}", path.display()))?;
            Ok(())
        }
    }
}

fn known_hosts_host_field(host: &str, port: u16) -> String {
    if port == 22 {
        host.to_string()
    } else {
        format!("[{host}]:{port}")
    }
}

fn host_key_type_name(kind: ssh2::HostKeyType) -> &'static str {
    use ssh2::HostKeyType::*;
    match kind {
        Rsa => "ssh-rsa",
        Dss => "ssh-dss",
        Ecdsa256 => "ecdsa-sha2-nistp256",
        Ecdsa384 => "ecdsa-sha2-nistp384",
        Ecdsa521 => "ecdsa-sha2-nistp521",
        Ed25519 => "ssh-ed25519",
        Unknown => "unknown",
    }
}

fn sha256_fingerprint(session: &Session) -> Option<String> {
    session
        .host_key_hash(HashType::Sha256)
        .map(|h| format!("SHA256:{}", URL_SAFE_NO_PAD.encode(h)))
}

/// Appends a single OpenSSH-format line to known_hosts without rewriting the
/// whole file (avoids clobbering unrelated entries/comments).
fn append_known_host(path: &Path, line: &str) -> Result<()> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)
            .with_context(|| format!("creando directorio {}", parent.display()))?;
    }
    let mut file = fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(path)
        .with_context(|| format!("abriendo {}", path.display()))?;
    file.write_all(line.as_bytes())
        .context("escribiendo entrada en known_hosts")?;
    Ok(())
}

/// Request sent from the UI thread to the SSH worker thread.
pub enum SshRequest {
    Connect {
        profile: ConnectionProfile,
        cols: u16,
        rows: u16,
    },
    Input(Vec<u8>),
    Resize(u16, u16),
    Upload {
        local: String,
        remote: String,
    },
    Read(String),
    Write {
        path: String,
        content: String,
    },
    Disconnect,
    Shutdown,
}

/// Event sent from the SSH worker thread back to the UI thread.
pub enum SshEvent {
    Connected { home: String },
    ConnectFailed(String),
    Output(Vec<u8>),
    ShellClosed,
    UploadDone { remote: String },
    FileContent { path: String, content: String },
    FileWritten { path: String },
    Failed { context: String, message: String },
    Disconnected,
    /// The interactive session died unexpectedly (shell read/write error). The UI
    /// must leave the session, otherwise it would keep showing a dead terminal
    /// where keystrokes go nowhere.
    SessionLost { message: String },
}

/// Spawns the SSH worker. All `SSHClient` state lives inside this thread, so the
/// UI never blocks on network I/O.
pub fn spawn_ssh_worker(rx: Receiver<SshRequest>, tx: Sender<SshEvent>) -> JoinHandle<()> {
    thread::spawn(move || {
        let mut client: Option<SSHClient> = None;
        // Typed bytes that the channel could not accept yet. Retried on the
        // next iteration, after reads have pumped libssh2.
        let mut pending: Vec<u8> = Vec::new();
        let mut last_pump = Instant::now();
        let mut quit = false;

        while !quit {
            // Not connected: block waiting for a Connect/Shutdown request.
            if client.is_none() {
                match rx.recv() {
                    Ok(SshRequest::Connect {
                        profile,
                        cols,
                        rows,
                    }) => match SSHClient::connect(&profile, cols, rows) {
                        Ok(c) => {
                            let home = c.home().to_string();
                            debug_log(&format!(
                                "worker: conectado a {}@{}:{} (home {home})",
                                profile.user, profile.host, profile.port
                            ));
                            let _ = tx.send(SshEvent::Connected { home });
                            client = Some(c);
                            pending.clear();
                        }
                        Err(e) => {
                            debug_log(&format!("worker: falló conexión: {e:#}"));
                            let _ = tx.send(SshEvent::ConnectFailed(format!("{e:#}")));
                        }
                    },
                    Ok(SshRequest::Shutdown) | Err(_) => quit = true,
                    Ok(_) => {}
                }
                continue;
            }

            // Connected: drain pending requests without blocking.
            let mut drop_client = false;
            let mut lost: Option<String> = None;
            loop {
                match rx.try_recv() {
                    Ok(SshRequest::Input(bytes)) => {
                        pending.extend_from_slice(&bytes);
                        if pending.len() > MAX_PENDING_INPUT {
                            debug_log(&format!(
                                "worker: buffer de entrada lleno ({} bytes)",
                                pending.len()
                            ));
                            lost = Some(
                                "el shell remoto dejó de aceptar datos".to_string(),
                            );
                            drop_client = true;
                            break;
                        }
                    }
                    Ok(SshRequest::Resize(cols, rows)) => {
                        if let Some(c) = client.as_mut() {
                            let _ = c.resize(cols, rows);
                        }
                    }
                    Ok(SshRequest::Upload { local, remote }) => {
                        if let Some(c) = client.as_mut() {
                            match c.upload_file(&local, &remote) {
                                Ok(()) => {
                                    let _ = tx.send(SshEvent::UploadDone { remote });
                                }
                                Err(e) => {
                                    let _ = tx.send(SshEvent::Failed {
                                        context: "subida".to_string(),
                                        message: format!("{e:#}"),
                                    });
                                }
                            }
                        }
                    }
                    Ok(SshRequest::Read(path)) => {
                        if let Some(c) = client.as_mut() {
                            match c.read_remote_file(&path) {
                                Ok(content) => {
                                    let _ = tx.send(SshEvent::FileContent { path, content });
                                }
                                Err(e) => {
                                    let _ = tx.send(SshEvent::Failed {
                                        context: "lectura".to_string(),
                                        message: format!("{e:#}"),
                                    });
                                }
                            }
                        }
                    }
                    Ok(SshRequest::Write { path, content }) => {
                        if let Some(c) = client.as_mut() {
                            match c.write_remote_file(&path, &content) {
                                Ok(()) => {
                                    let _ = tx.send(SshEvent::FileWritten { path });
                                }
                                Err(e) => {
                                    let _ = tx.send(SshEvent::Failed {
                                        context: "escritura".to_string(),
                                        message: format!("{e:#}"),
                                    });
                                }
                            }
                        }
                    }
                    Ok(SshRequest::Disconnect) => {
                        drop_client = true;
                        break;
                    }
                    Ok(SshRequest::Shutdown) => {
                        quit = true;
                        drop_client = true;
                        break;
                    }
                    Ok(SshRequest::Connect { .. }) => {}
                    Err(TryRecvError::Empty) => break,
                    Err(TryRecvError::Disconnected) => {
                        quit = true;
                        drop_client = true;
                        break;
                    }
                }
            }

            // Flush buffered keystrokes without blocking. Reads happen right
            // after this, which is what lets libssh2 drain its window updates.
            if !drop_client && !pending.is_empty() {
                if let Some(c) = client.as_mut() {
                    match c.try_write(&pending) {
                        Ok(0) => {}
                        Ok(n) => {
                            pending.drain(..n);
                        }
                        Err(e) => {
                            debug_log(&format!("worker: fallo al escribir: {e:#}"));
                            lost = Some(format!("entrada: {e:#}"));
                            drop_client = true;
                        }
                    }
                }
            }

            if drop_client {
                pending.clear();
                if let Some(c) = client.as_mut() {
                    c.close_shell();
                }
                client = None;
                match lost.take() {
                    Some(message) => {
                        debug_log(&format!("worker: sesión perdida: {message}"));
                        let _ = tx.send(SshEvent::SessionLost { message });
                    }
                    None => {
                        debug_log("worker: desconexión solicitada");
                        let _ = tx.send(SshEvent::Disconnected);
                    }
                }
                continue;
            }

            // Read available shell output.
            if let Some(c) = client.as_mut() {
                match c.poll_shell() {
                    Ok(ShellPoll::Data(data)) => {
                        if !data.is_empty() {
                            let _ = tx.send(SshEvent::Output(data));
                        }
                    }
                    Ok(ShellPoll::Eof) => {
                        debug_log("worker: el shell remoto cerró (EOF)");
                        let _ = tx.send(SshEvent::ShellClosed);
                        client = None;
                        pending.clear();
                        continue;
                    }
                    Ok(ShellPoll::Idle) => {
                        // libssh2 reports an idle read even after the peer has
                        // closed the socket; check the raw connection so a dead
                        // link is reported now, not on the next keystroke.
                        if c.peer_closed() {
                            debug_log("worker: el peer cerró la conexión TCP");
                            let _ = tx.send(SshEvent::SessionLost {
                                message: "la conexión TCP se cerró".to_string(),
                            });
                            client = None;
                            pending.clear();
                            continue;
                        }
                    }
                    Err(e) => {
                        debug_log(&format!("worker: error de shell: {e:#}"));
                        let _ = tx.send(SshEvent::SessionLost {
                            message: format!("shell: {e:#}"),
                        });
                        client = None;
                        pending.clear();
                        continue;
                    }
                }
            }

            // Periodically pump the session so a link that is silent stays
            // alive and libssh2 flushes whatever it has queued.
            if let Some(c) = client.as_mut() {
                if last_pump.elapsed() >= Duration::from_secs(1) {
                    c.keepalive();
                    last_pump = Instant::now();
                }
            }

            thread::sleep(Duration::from_millis(8));
        }
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn normalizes_paths() {
        assert_eq!(normalize_posix_path("/a/b/../c"), "/a/c");
        assert_eq!(normalize_posix_path("/a/./b/"), "/a/b");
        assert_eq!(normalize_posix_path("/"), "/");
    }

    #[test]
    fn known_hosts_field_uses_brackets_for_custom_port() {
        assert_eq!(known_hosts_host_field("h", 22), "h");
        assert_eq!(known_hosts_host_field("h", 2222), "[h]:2222");
    }
}
