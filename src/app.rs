use std::fs;
use std::io;
use std::path::Path;
use std::sync::mpsc::{self, Receiver, Sender};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use anyhow::Result;
use crossterm::event::{self, Event, KeyCode, KeyEvent, KeyEventKind, KeyModifiers};
use crossterm::terminal::{
    disable_raw_mode, enable_raw_mode, EnterAlternateScreen, LeaveAlternateScreen,
};
use crossterm::{execute, ExecutableCommand};
use ratatui::prelude::*;
use tui_textarea::{Input, Key, TextArea};

use crate::ssh::{debug_log, spawn_ssh_worker, HostKeyInfo, SshEvent, SshRequest};
use crate::store::{AuthMethod, ConnectionProfile, SecureStore};
use crate::ui;

/// The one prefix key used by every app action in every mode. A single prefix
/// means no plain letter or Ctrl+letter ever triggers an action by accident.
const PREFIX_KEY: char = 'q';

/// How long the prefix stays armed waiting for its command key.
const PREFIX_TIMEOUT: Duration = Duration::from_secs(4);

/// Minimum time an error stays visible. Without this, the keystrokes the user
/// keeps typing after a session drop erase the explanation instantly.
const ERROR_MIN_DISPLAY: Duration = Duration::from_secs(6);

const LIST_STATUS: &str = "Enter: conectar · Ctrl+Q: acciones · Ctrl+C: salir";

const LIST_HELP: &str =
    "Prefijo Ctrl+Q: n=nuevo · e=editar · d=borrar · x=exportar · r=importar · q=salir";

/// Session commands. Disconnecting needs the `Ctrl` modifier on the second
/// key on purpose: a bare letter after a stray prefix (for example the `d` of
/// `cd /var/www/...`) must never end the session.
const SESSION_HELP: &str =
    "Prefijo Ctrl+Q: Ctrl+D=desconectar · u=subir archivo · r=abrir/editar remoto · ?=ayuda";

#[derive(Clone)]
pub(crate) struct FormField {
    pub(crate) key: &'static str,
    pub(crate) label: &'static str,
    pub(crate) value: String,
    pub(crate) secret: bool,
}

#[derive(Clone)]
pub(crate) struct ProfileForm {
    pub(crate) edit_index: Option<usize>,
    pub(crate) index: usize,
    pub(crate) fields: Vec<FormField>,
}

impl ProfileForm {
    fn from_profile(edit_index: Option<usize>, p: Option<&ConnectionProfile>) -> Self {
        let p = p.cloned().unwrap_or_else(|| ConnectionProfile {
            port: 22,
            auth_method: AuthMethod::Password,
            ..Default::default()
        });
        Self {
            edit_index,
            index: 0,
            fields: vec![
                FormField {
                    key: "name",
                    label: "Nombre",
                    value: p.name,
                    secret: false,
                },
                FormField {
                    key: "host",
                    label: "Host",
                    value: p.host,
                    secret: false,
                },
                FormField {
                    key: "port",
                    label: "Puerto",
                    value: p.port.to_string(),
                    secret: false,
                },
                FormField {
                    key: "user",
                    label: "Usuario",
                    value: p.user,
                    secret: false,
                },
                FormField {
                    key: "auth",
                    label: "Auth (password|key|both|interactive)",
                    value: match p.auth_method {
                        AuthMethod::Password => "password".to_string(),
                        AuthMethod::Key => "key".to_string(),
                        AuthMethod::Both => "both".to_string(),
                        AuthMethod::Interactive => "interactive".to_string(),
                    },
                    secret: false,
                },
                FormField {
                    key: "password",
                    label: "Password",
                    value: p.password,
                    secret: true,
                },
                FormField {
                    key: "key_path",
                    label: "Ruta private key (~/.ssh/id_ed25519)",
                    value: p.private_key_path,
                    secret: false,
                },
                FormField {
                    key: "key_pass",
                    label: "Passphrase key",
                    value: p.private_key_pass,
                    secret: true,
                },
            ],
        }
    }

    fn current_mut(&mut self) -> &mut String {
        &mut self.fields[self.index].value
    }

    fn next(&mut self) {
        self.index = (self.index + 1) % self.fields.len();
    }

    fn prev(&mut self) {
        self.index = if self.index == 0 {
            self.fields.len() - 1
        } else {
            self.index - 1
        };
    }

    fn build_profile(&self) -> Result<ConnectionProfile> {
        let get = |k: &str| {
            self.fields
                .iter()
                .find(|f| f.key == k)
                .map(|f| f.value.trim().to_string())
                .unwrap_or_default()
        };

        let name = get("name");
        let host = get("host");
        let user = get("user");
        if name.is_empty() || host.is_empty() || user.is_empty() {
            anyhow::bail!("nombre, host y usuario son obligatorios");
        }
        let port: u16 = get("port")
            .parse()
            .map_err(|_| anyhow::anyhow!("puerto inválido"))?;
        let auth_text = get("auth").to_lowercase();
        let auth_method = match auth_text.as_str() {
            "" | "password" => AuthMethod::Password,
            "key" => AuthMethod::Key,
            "both" => AuthMethod::Both,
            "interactive" | "kbi" => AuthMethod::Interactive,
            _ => anyhow::bail!("auth inválida: usa password, key, both o interactive"),
        };
        let password = get("password");
        let private_key_path = get("key_path");
        let private_key_pass = get("key_pass");

        if matches!(auth_method, AuthMethod::Password | AuthMethod::Interactive)
            && password.is_empty()
        {
            anyhow::bail!("password requerido para auth=password/interactive");
        }
        if matches!(auth_method, AuthMethod::Key | AuthMethod::Both) && private_key_path.is_empty()
        {
            anyhow::bail!("private key path requerido para auth=key/both");
        }

        Ok(ConnectionProfile {
            name,
            host,
            port,
            user,
            auth_method,
            password,
            private_key_path,
            private_key_pass,
        })
    }
}

pub(crate) enum PromptKind {
    Upload,
    OpenRemote,
}

pub(crate) struct PromptState {
    pub(crate) title: String,
    pub(crate) input: String,
    pub(crate) kind: PromptKind,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum BackupKind {
    Export,
    Import,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum BackupStep {
    Path,
    Pass,
    Confirm,
}

pub(crate) struct BackupState {
    pub(crate) kind: BackupKind,
    pub(crate) step: BackupStep,
    pub(crate) path: String,
    pub(crate) pass: String,
    pub(crate) input: String,
    pub(crate) message: String,
    pub(crate) error: Option<String>,
}

/// Pending trust-on-first-use decision for an unknown host key.
pub(crate) struct HostKeyPrompt {
    pub(crate) info: HostKeyInfo,
    pub(crate) input: String,
}

pub(crate) enum Mode {
    List,
    HostKey(HostKeyPrompt),
    Form(ProfileForm),
    Session,
    Prompt(PromptState),
    FileEdit,
    Backup(BackupState),
}

pub struct App {
    pub(crate) store: SecureStore,
    pub(crate) profiles: Vec<ConnectionProfile>,
    pub(crate) selected: usize,
    pub(crate) mode: Mode,
    pub(crate) status: String,
    pub(crate) error: Option<String>,
    /// Errors stay on screen at least until this instant, so a session loss is
    /// not wiped by the keystrokes the user keeps typing after it.
    error_until: Option<Instant>,
    pub(crate) should_quit: bool,
    pub(crate) tick: usize,

    ssh_tx: Sender<SshRequest>,
    ssh_rx: Receiver<SshEvent>,
    pub(crate) connecting: bool,
    pub(crate) connected: bool,
    pub(crate) prefix: bool,
    /// When the prefix was armed, used to auto-cancel a stray prefix.
    prefix_at: Instant,
    pub(crate) parser: vt100::Parser,

    pub(crate) term_cols: u16,
    pub(crate) term_rows: u16,

    pub(crate) editor: Option<TextArea<'static>>,
    pub(crate) edit_path: String,
}

impl App {
    pub fn new(store: SecureStore, profiles: Vec<ConnectionProfile>) -> Self {
        let (req_tx, req_rx) = mpsc::channel();
        let (evt_tx, evt_rx) = mpsc::channel();
        spawn_ssh_worker(req_rx, evt_tx);
        let (cols, rows) = crossterm::terminal::size().unwrap_or((80, 24));
        Self {
            store,
            profiles,
            selected: 0,
            mode: Mode::List,
            status: LIST_STATUS.to_string(),
            error: None,
            error_until: None,
            should_quit: false,
            tick: 0,
            ssh_tx: req_tx,
            ssh_rx: evt_rx,
            connecting: false,
            connected: false,
            prefix: false,
            prefix_at: Instant::now(),
            parser: vt100::Parser::new(24, 80, 0),
            term_cols: cols,
            term_rows: rows,
            editor: None,
            edit_path: String::new(),
        }
    }

    pub fn run(&mut self) -> Result<()> {
        enable_raw_mode()?;
        io::stdout().execute(EnterAlternateScreen)?;
        let backend = ratatui::backend::CrosstermBackend::new(io::stdout());
        let mut terminal = Terminal::new(backend)?;
        terminal.clear()?;

        let result = self.event_loop(&mut terminal);

        disable_raw_mode()?;
        execute!(io::stdout(), LeaveAlternateScreen)?;
        terminal.show_cursor()?;

        let _ = self.ssh_tx.send(SshRequest::Shutdown);
        result
    }

    fn event_loop(
        &mut self,
        terminal: &mut Terminal<ratatui::backend::CrosstermBackend<io::Stdout>>,
    ) -> Result<()> {
        let mut dirty = true;
        while !self.should_quit {
            dirty |= self.drain_ssh_events();
            dirty |= self.expire_prefix();
            // The spinner animates while connecting; otherwise only repaint when
            // something changed, instead of rebuilding the frame every 50 ms.
            if dirty || self.connecting {
                terminal.draw(|f| ui::draw(self, f))?;
                dirty = false;
            }
            let mut wait = Duration::from_millis(50);
            // Drain every queued input event before repainting so a paste or key
            // repeat does not render once per key.
            while event::poll(wait)? {
                dirty = true;
                match event::read()? {
                    Event::Key(key) => {
                        if key.kind != KeyEventKind::Release {
                            self.handle_key(key);
                        }
                    }
                    Event::Resize(w, h) => self.handle_resize(w, h),
                    _ => {}
                }
                if self.should_quit {
                    break;
                }
                wait = Duration::ZERO;
            }
            self.tick = self.tick.wrapping_add(1);
        }
        Ok(())
    }

    fn handle_resize(&mut self, w: u16, h: u16) {
        self.term_cols = w;
        self.term_rows = h;
        if self.connected {
            let (cols, rows) = ui::session_inner_size(w, h);
            self.parser.set_size(rows, cols);
            let _ = self.ssh_tx.send(SshRequest::Resize(cols, rows));
        }
    }

    /// Applies every queued worker event; returns whether any was received.
    fn drain_ssh_events(&mut self) -> bool {
        let mut any = false;
        while let Ok(ev) = self.ssh_rx.try_recv() {
            self.apply_ssh_event(ev);
            any = true;
        }
        any
    }

    fn apply_ssh_event(&mut self, ev: SshEvent) {
        if !matches!(ev, SshEvent::Output(_)) {
            debug_log(&format!("app: evento {}", ssh_event_name(&ev)));
        }
        match ev {
            SshEvent::Connected { home } => {
                self.connecting = false;
                self.connected = true;
                let (cols, rows) = ui::session_inner_size(self.term_cols, self.term_rows);
                // Fresh screen so the previous session's buffer does not linger.
                self.parser = vt100::Parser::new(rows, cols, 0);
                let _ = self.ssh_tx.send(SshRequest::Resize(cols, rows));
                self.mode = Mode::Session;
                self.status = format!("Conectado · home {home} · {SESSION_HELP}");
            }
            SshEvent::ConfirmHostKey(info) => {
                self.prefix = false;
                self.status = "Host desconocido: verifica la huella antes de continuar".to_string();
                self.mode = Mode::HostKey(HostKeyPrompt {
                    info,
                    input: String::new(),
                });
            }
            SshEvent::ConnectFailed(msg) => {
                self.mode = Mode::List;
                self.connecting = false;
                self.connected = false;
                self.set_error(format!("falló conexión: {msg}"));
            }
            SshEvent::Output(bytes) => {
                self.parser.process(&bytes);
            }
            SshEvent::ShellClosed => {
                self.connected = false;
                self.mode = Mode::List;
                self.status = "la sesión remota se cerró".to_string();
            }
            SshEvent::UploadDone { remote } => {
                self.status = format!("archivo subido: {remote}");
            }
            SshEvent::FileContent { path, content } => {
                let lines: Vec<String> = if content.is_empty() {
                    vec![String::new()]
                } else {
                    content.lines().map(|l| l.to_string()).collect()
                };
                let mut ta = TextArea::from(lines);
                ta.set_cursor_line_style(Style::default().fg(Color::Yellow));
                self.editor = Some(ta);
                self.edit_path = path;
                self.mode = Mode::FileEdit;
                self.status = "Ctrl+S: guardar · Esc: cancelar".to_string();
            }
            SshEvent::FileWritten { path } => {
                self.mode = Mode::Session;
                self.status = format!("archivo remoto guardado: {path}");
            }
            SshEvent::Failed { context, message } => {
                self.set_error(format!("{context}: {message}"));
            }
            SshEvent::Disconnected => {
                self.connected = false;
                if matches!(self.mode, Mode::Session) {
                    self.mode = Mode::List;
                    self.status = "desconectado".to_string();
                }
            }
            SshEvent::SessionLost { message } => {
                self.on_session_lost(&format!("sesión finalizada: {message}"));
            }
        }
    }

    fn handle_key(&mut self, key: KeyEvent) {
        // Keep a fresh error on screen for a moment instead of letting the
        // next keystroke erase it.
        if self.error_until.map_or(true, |t| Instant::now() >= t) {
            self.error = None;
            self.error_until = None;
        }
        match &mut self.mode {
            Mode::List => self.handle_list_keys(key),
            Mode::Session => self.handle_session_keys(key),
            Mode::HostKey(_) => self.handle_host_key_keys(key),
            Mode::Form(_) => self.handle_form_keys(key),
            Mode::Prompt(_) => self.handle_prompt_keys(key),
            Mode::FileEdit => self.handle_file_edit_keys(key),
            Mode::Backup(_) => self.handle_backup_keys(key),
        }
    }

    fn handle_list_keys(&mut self, key: KeyEvent) {
        let ctrl = key.modifiers.contains(KeyModifiers::CONTROL);

        // Emergency exit: always available, even if a flow gets stuck.
        if ctrl && key.code == KeyCode::Char('c') {
            self.should_quit = true;
            return;
        }

        // A prefix command is pending: consume this key as the command.
        if self.prefix {
            self.prefix = false;
            match key.code {
                KeyCode::Char('n') if !ctrl => {
                    self.mode = Mode::Form(ProfileForm::from_profile(None, None));
                    self.status = "Tab/Shift+Tab para moverte, Enter guardar, Esc cancelar"
                        .to_string();
                }
                KeyCode::Char('e') if !ctrl => {
                    if let Some(p) = self.profiles.get(self.selected) {
                        self.mode = Mode::Form(ProfileForm::from_profile(Some(self.selected), Some(p)));
                        self.status = "Editando perfil".to_string();
                    }
                }
                KeyCode::Char('d') if !ctrl => {
                    if self.selected < self.profiles.len() {
                        self.profiles.remove(self.selected);
                        if self.selected >= self.profiles.len() && self.selected > 0 {
                            self.selected -= 1;
                        }
                        if let Err(e) = self.store.save_profiles(&self.profiles) {
                            self.set_error(e.to_string());
                        } else {
                            self.status = "perfil eliminado".to_string();
                        }
                    }
                }
                KeyCode::Char('x') if !ctrl => self.start_backup_export(),
                KeyCode::Char('r') if !ctrl => self.start_backup_import(),
                KeyCode::Char('q') if !ctrl => self.should_quit = true,
                KeyCode::Esc => self.status = LIST_STATUS.to_string(),
                _ => {
                    self.status = format!("prefijo: comando desconocido · {LIST_HELP}");
                }
            }
            return;
        }

        if is_prefix_key(&key) {
            self.begin_prefix(LIST_HELP);
            return;
        }

        match key.code {
            KeyCode::Up => {
                if self.selected > 0 {
                    self.selected -= 1;
                }
            }
            KeyCode::Down => {
                if self.selected + 1 < self.profiles.len() {
                    self.selected += 1;
                }
            }
            KeyCode::Enter => {
                if let Some(profile) = self.profiles.get(self.selected).cloned() {
                    self.connecting = true;
                    self.connected = false;
                    self.prefix = false;
                    self.mode = Mode::Session;
                    self.status = format!(
                        "conectando a {}@{}:{}...",
                        profile.user, profile.host, profile.port
                    );
                    let (cols, rows) = ui::session_inner_size(self.term_cols, self.term_rows);
                    self.parser = vt100::Parser::new(rows, cols, 0);
                    let _ = self.ssh_tx.send(SshRequest::Connect {
                        profile,
                        cols,
                        rows,
                    });
                }
            }
            _ => {}
        }
    }

    fn handle_session_keys(&mut self, key: KeyEvent) {
        if self.connecting {
            if key.modifiers.contains(KeyModifiers::CONTROL)
                && matches!(key.code, KeyCode::Char('c') | KeyCode::Char('q'))
            {
                self.should_quit = true;
            }
            return;
        }

        let ctrl = key.modifiers.contains(KeyModifiers::CONTROL);

        // A prefix command is pending: consume this key as the command.
        if self.prefix {
            self.prefix = false;
            // Ctrl+D is the only way to leave the session: it needs the Ctrl
            // modifier, so no typed character can ever trigger it.
            if ctrl && key.code == KeyCode::Char('d') {
                debug_log("app: prefijo → desconectar (Ctrl+Q Ctrl+D)");
                self.disconnect();
                return;
            }
            match key.code {
                KeyCode::Char('u') if !ctrl => {
                    debug_log("app: prefijo → subir archivo");
                    self.mode = Mode::Prompt(PromptState {
                        title: "Subir archivo: local_path remote_path".to_string(),
                        input: String::new(),
                        kind: PromptKind::Upload,
                    });
                }
                KeyCode::Char('r') | KeyCode::Char('o') if !ctrl => {
                    debug_log("app: prefijo → abrir/editar archivo remoto");
                    self.mode = Mode::Prompt(PromptState {
                        title: "Abrir archivo remoto: /ruta/al/archivo".to_string(),
                        input: String::new(),
                        kind: PromptKind::OpenRemote,
                    });
                }
                KeyCode::Char('?') => self.status = SESSION_HELP.to_string(),
                KeyCode::Esc => self.status = "prefijo cancelado".to_string(),
                // Not an app command: never swallow the keystroke, send it to the
                // remote instead (this also lets Ctrl+Q after the prefix reach the
                // remote as XON if the server re-enabled flow control).
                _ => {
                    self.status = SESSION_HELP.to_string();
                    self.send_session_key(key);
                }
            }
            return;
        }

        if is_prefix_key(&key) {
            self.begin_prefix(SESSION_HELP);
            return;
        }

        self.send_session_key(key);
    }

    /// Forwards a key to the remote shell, surfacing a dead worker instead of
    /// silently dropping input.
    fn send_session_key(&mut self, key: KeyEvent) {
        if let Some(bytes) = encode_key(key) {
            debug_log(&format!("app: enviando {} byte(s) al shell", bytes.len()));
            if self.ssh_tx.send(SshRequest::Input(bytes)).is_err() {
                self.on_session_lost("la conexión con el proceso SSH se perdió");
            }
        }
    }

    /// Shows an important message from startup (long enough to read a path).
    pub fn set_startup_notice(&mut self, message: String) {
        self.error = Some(message);
        self.error_until = Some(Instant::now() + Duration::from_secs(45));
    }

    /// Shows an error and guarantees it stays visible for a minimum time.
    fn set_error(&mut self, message: impl Into<String>) {
        self.error = Some(message.into());
        self.error_until = Some(Instant::now() + ERROR_MIN_DISPLAY);
    }

    fn on_session_lost(&mut self, message: &str) {
        debug_log(&format!("app: on_session_lost: {message}"));
        self.connected = false;
        self.connecting = false;
        self.prefix = false;
        self.mode = Mode::List;
        self.set_error(message.to_string());
        self.status = LIST_STATUS.to_string();
    }

    fn begin_prefix(&mut self, help: &str) {
        debug_log("app: prefijo Ctrl+Q armado");
        self.prefix = true;
        self.prefix_at = Instant::now();
        self.status = format!("PREFIJO activo · {help}");
    }

    /// Disarms a stale prefix; returns whether the screen needs a repaint.
    fn expire_prefix(&mut self) -> bool {
        if self.prefix && self.prefix_at.elapsed() > PREFIX_TIMEOUT {
            self.prefix = false;
            self.status = self.default_status().to_string();
            return true;
        }
        false
    }

    fn default_status(&self) -> &'static str {
        match self.mode {
            Mode::Session => SESSION_HELP,
            _ => LIST_STATUS,
        }
    }

    fn disconnect(&mut self) {
        debug_log("app: desconectar solicitado por el usuario");
        let _ = self.ssh_tx.send(SshRequest::Disconnect);
        self.connected = false;
        self.prefix = false;
        self.mode = Mode::List;
        self.status = "desconectado".to_string();
    }

    /// Only an explicit `yes` trusts the host; anything else (Esc, Ctrl+C, a
    /// different answer) rejects it, so a stray Enter can never accept a key.
    fn handle_host_key_keys(&mut self, key: KeyEvent) {
        let Mode::HostKey(prompt) = &mut self.mode else {
            return;
        };
        let ctrl = key.modifiers.contains(KeyModifiers::CONTROL);
        let reject = key.code == KeyCode::Esc || (ctrl && key.code == KeyCode::Char('c'));
        if !reject && key.code != KeyCode::Enter {
            edit_line_tail(&mut prompt.input, key);
            return;
        }
        let accept = !reject && prompt.input.trim().eq_ignore_ascii_case("yes");
        debug_log(&format!("app: host key {}", if accept { "aceptada" } else { "rechazada" }));
        self.mode = Mode::Session;
        self.status = if accept {
            "Host key aceptada · autenticando...".to_string()
        } else {
            "Host key rechazada · cancelando conexión...".to_string()
        };
        let _ = self.ssh_tx.send(SshRequest::HostKeyDecision(accept));
    }

    fn handle_form_keys(&mut self, key: KeyEvent) {
        let Mode::Form(form) = &mut self.mode else {
            return;
        };
        match key.code {
            KeyCode::Esc => {
                self.mode = Mode::List;
                self.status = "edición cancelada".to_string();
            }
            KeyCode::Tab => form.next(),
            KeyCode::BackTab => form.prev(),
            KeyCode::Enter => match form.build_profile() {
                Ok(profile) => {
                    if let Some(idx) = form.edit_index {
                        if idx < self.profiles.len() {
                            self.profiles[idx] = profile;
                        }
                    } else {
                        self.profiles.push(profile);
                        self.selected = self.profiles.len().saturating_sub(1);
                    }
                    if let Err(e) = self.store.save_profiles(&self.profiles) {
                        self.set_error(e.to_string());
                    } else {
                        self.mode = Mode::List;
                        self.status = "perfil guardado".to_string();
                    }
                }
                Err(e) => {
                    self.set_error(e.to_string());
                }
            },
            _ => {
                let value = form.current_mut();
                edit_line_tail(value, key);
            }
        }
    }

    fn handle_prompt_keys(&mut self, key: KeyEvent) {
        let Mode::Prompt(prompt) = &mut self.mode else {
            return;
        };
        if key.code == KeyCode::Esc {
            self.mode = Mode::Session;
            self.status = "cancelado".to_string();
            return;
        }
        if key.code == KeyCode::Enter {
            let input = prompt.input.trim().to_string();
            let kind = std::mem::replace(&mut prompt.kind, PromptKind::Upload);
            self.mode = Mode::Session;
            match kind {
                PromptKind::Upload => {
                    let parts: Vec<&str> = input.split_whitespace().collect();
                    if parts.len() != 2 {
                        self.set_error("usa: local_path remote_path");
                        return;
                    }
                    self.status = "subiendo archivo...".to_string();
                    let _ = self.ssh_tx.send(SshRequest::Upload {
                        local: parts[0].to_string(),
                        remote: parts[1].to_string(),
                    });
                }
                PromptKind::OpenRemote => {
                    if input.is_empty() {
                        self.set_error("ruta requerida");
                        return;
                    }
                    self.status = "leyendo archivo remoto...".to_string();
                    let _ = self.ssh_tx.send(SshRequest::Read(input));
                }
            }
            return;
        }
        let mut cursor = prompt.input.len();
        edit_line(&mut prompt.input, &mut cursor, key);
    }

    fn handle_file_edit_keys(&mut self, key: KeyEvent) {
        if key.code == KeyCode::Esc {
            self.mode = Mode::Session;
            self.status = "edición cancelada".to_string();
            return;
        }
        if key.modifiers.contains(KeyModifiers::CONTROL) && key.code == KeyCode::Char('s') {
            let content = if let Some(editor) = self.editor.as_ref() {
                editor.lines().join("\n")
            } else {
                String::new()
            };
            let path = self.edit_path.clone();
            self.status = "guardando archivo remoto...".to_string();
            let _ = self.ssh_tx.send(SshRequest::Write { path, content });
            return;
        }

        if let Some(editor) = self.editor.as_mut() {
            editor.input(Input::from(to_textarea_key(key)));
        }
    }

    // ---- Backup export/import (passphrase-protected) ----

    fn start_backup_export(&mut self) {
        let path = default_backup_path();
        self.mode = Mode::Backup(BackupState {
            kind: BackupKind::Export,
            step: BackupStep::Path,
            path: String::new(),
            pass: String::new(),
            input: path,
            message: "Ruta de destino del backup (Enter para aceptar)".to_string(),
            error: None,
        });
        self.status = "Exportar backup".to_string();
    }

    fn start_backup_import(&mut self) {
        self.mode = Mode::Backup(BackupState {
            kind: BackupKind::Import,
            step: BackupStep::Path,
            path: String::new(),
            pass: String::new(),
            input: String::new(),
            message: "Ruta del archivo de backup a importar".to_string(),
            error: None,
        });
        self.status = "Importar backup".to_string();
    }

    fn set_backup_error(&mut self, msg: &str) {
        if let Mode::Backup(state) = &mut self.mode {
            state.error = Some(msg.to_string());
        }
    }

    fn handle_backup_keys(&mut self, key: KeyEvent) {
        let (kind, step, input) = match &self.mode {
            Mode::Backup(state) => (state.kind, state.step, state.input.trim().to_string()),
            _ => return,
        };

        if key.code == KeyCode::Esc {
            self.mode = Mode::List;
            self.status = "backup cancelado".to_string();
            return;
        }
        if key.code != KeyCode::Enter {
            if let Mode::Backup(state) = &mut self.mode {
                let mut cursor = state.input.len();
                edit_line(&mut state.input, &mut cursor, key);
            }
            return;
        }

        match step {
            BackupStep::Path => {
                if input.is_empty() {
                    self.set_backup_error("ruta requerida");
                    return;
                }
                let resolved = expand_path(&input);
                if kind == BackupKind::Import && !Path::new(&resolved).exists() {
                    self.set_backup_error(&format!("no existe el archivo {input}"));
                    return;
                }
                if let Mode::Backup(state) = &mut self.mode {
                    state.path = input;
                    state.step = BackupStep::Pass;
                    state.input.clear();
                    state.error = None;
                    state.message = match kind {
                        BackupKind::Export => "Contraseña para proteger el backup".to_string(),
                        BackupKind::Import => "Contraseña del backup".to_string(),
                    };
                }
            }
            BackupStep::Pass => {
                if input.is_empty() {
                    self.set_backup_error("la contraseña no puede estar vacía");
                    return;
                }
                match kind {
                    BackupKind::Export => {
                        if input.chars().count() < crate::store::MIN_BACKUP_PASSPHRASE_CHARS {
                            self.set_backup_error(&format!(
                                "usa al menos {} caracteres (protege todas tus contraseñas)",
                                crate::store::MIN_BACKUP_PASSPHRASE_CHARS
                            ));
                            if let Mode::Backup(state) = &mut self.mode {
                                state.input.clear();
                            }
                            return;
                        }
                        if let Mode::Backup(state) = &mut self.mode {
                            state.pass = input;
                            state.step = BackupStep::Confirm;
                            state.input.clear();
                            state.error = None;
                            state.message = "Repite la contraseña".to_string();
                        }
                    }
                    BackupKind::Import => self.finish_import(&input),
                }
            }
            BackupStep::Confirm => {
                let pass = match &self.mode {
                    Mode::Backup(state) => state.pass.clone(),
                    _ => return,
                };
                if input != pass {
                    self.set_backup_error("las contraseñas no coinciden");
                    if let Mode::Backup(state) = &mut self.mode {
                        state.input.clear();
                    }
                    return;
                }
                let path = match &self.mode {
                    Mode::Backup(state) => state.path.clone(),
                    _ => return,
                };
                self.finish_export(&path, &pass);
            }
        }
    }

    fn finish_export(&mut self, path: &str, pass: &str) {
        let resolved = expand_path(path);
        match crate::store::export_backup(&self.profiles, pass) {
            Ok(json) => match crate::store::write_private(Path::new(&resolved), json.as_bytes()) {
                Ok(()) => {
                    self.mode = Mode::List;
                    self.status = format!(
                        "backup exportado ({} perfiles) → {resolved}",
                        self.profiles.len()
                    );
                }
                Err(e) => {
                    self.mode = Mode::List;
                    self.set_error(format!("escribiendo backup: {e:#}"));
                }
            },
            Err(e) => {
                self.mode = Mode::List;
                self.set_error(format!("{e:#}"));
            }
        }
    }

    fn finish_import(&mut self, pass: &str) {
        let path = match &self.mode {
            Mode::Backup(state) => state.path.clone(),
            _ => return,
        };
        let resolved = expand_path(&path);
        let content = match fs::read_to_string(&resolved) {
            Ok(c) => c,
            Err(e) => {
                self.mode = Mode::List;
                self.set_error(format!("leyendo {resolved}: {e}"));
                return;
            }
        };
        match crate::store::import_backup(&content, pass) {
            Ok(imported) => {
                let mut added = 0usize;
                let mut skipped = 0usize;
                for p in imported {
                    let duplicate = self.profiles.iter().any(|e| {
                        e.name == p.name && e.host == p.host && e.port == p.port && e.user == p.user
                    });
                    if duplicate {
                        skipped += 1;
                    } else {
                        self.profiles.push(p);
                        added += 1;
                    }
                }
                if added > 0 {
                    if let Err(e) = self.store.save_profiles(&self.profiles) {
                        self.set_error(format!("guardando perfiles importados: {e}"));
                    }
                }
                self.mode = Mode::List;
                self.status =
                    format!("backup importado: {added} nuevas · {skipped} ya existían");
            }
            Err(e) => {
                // Keep the passphrase step so the user can retry.
                if let Mode::Backup(state) = &mut self.mode {
                    state.error = Some(format!("{e:#}"));
                    state.input.clear();
                }
            }
        }
    }
}

/// Expands a leading `~/` using the user's home directory.
fn expand_path(p: &str) -> String {
    if let Some(rest) = p.strip_prefix("~/") {
        if let Some(home) = dirs::home_dir() {
            return home.join(rest).to_string_lossy().to_string();
        }
    }
    p.to_string()
}

fn default_backup_path() -> String {
    let ts = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let name = format!("tuissh-backup-{ts}.json");
    match dirs::home_dir() {
        Some(home) => home.join(name).to_string_lossy().to_string(),
        None => name,
    }
}

/// True when the key is the single app prefix (`Ctrl+Q`).
fn is_prefix_key(key: &KeyEvent) -> bool {
    key.modifiers.contains(KeyModifiers::CONTROL) && key.code == KeyCode::Char(PREFIX_KEY)
}

/// Human-readable name for a worker event, used in the diagnostic log.
fn ssh_event_name(ev: &SshEvent) -> &'static str {
    match ev {
        SshEvent::Connected { .. } => "Connected",
        SshEvent::ConfirmHostKey(_) => "ConfirmHostKey",
        SshEvent::ConnectFailed(_) => "ConnectFailed",
        SshEvent::Output(_) => "Output",
        SshEvent::ShellClosed => "ShellClosed",
        SshEvent::UploadDone { .. } => "UploadDone",
        SshEvent::FileContent { .. } => "FileContent",
        SshEvent::FileWritten { .. } => "FileWritten",
        SshEvent::Failed { .. } => "Failed",
        SshEvent::Disconnected => "Disconnected",
        SshEvent::SessionLost { .. } => "SessionLost",
    }
}

/// Encodes a crossterm key event into the byte sequence a PTY expects.
fn encode_key(key: KeyEvent) -> Option<Vec<u8>> {
    let ctrl = key.modifiers.contains(KeyModifiers::CONTROL);
    let alt = key.modifiers.contains(KeyModifiers::ALT);
    let mut out: Vec<u8> = Vec::new();
    if alt {
        out.push(0x1b);
    }
    match key.code {
        KeyCode::Char(c) => {
            if ctrl {
                out.push(ctrl_byte(c)?);
            } else {
                let mut buf = [0u8; 4];
                out.extend_from_slice(c.encode_utf8(&mut buf).as_bytes());
            }
        }
        KeyCode::Enter => out.push(b'\r'),
        KeyCode::Backspace => out.push(0x7f),
        KeyCode::Tab => out.push(b'\t'),
        KeyCode::BackTab => out.extend_from_slice(b"\x1b[Z"),
        KeyCode::Esc => out.push(0x1b),
        KeyCode::Up => out.extend_from_slice(b"\x1b[A"),
        KeyCode::Down => out.extend_from_slice(b"\x1b[B"),
        KeyCode::Right => out.extend_from_slice(b"\x1b[C"),
        KeyCode::Left => out.extend_from_slice(b"\x1b[D"),
        KeyCode::Home => out.extend_from_slice(b"\x1b[H"),
        KeyCode::End => out.extend_from_slice(b"\x1b[F"),
        KeyCode::PageUp => out.extend_from_slice(b"\x1b[5~"),
        KeyCode::PageDown => out.extend_from_slice(b"\x1b[6~"),
        KeyCode::Insert => out.extend_from_slice(b"\x1b[2~"),
        KeyCode::Delete => out.extend_from_slice(b"\x1b[3~"),
        KeyCode::F(n) => out.extend_from_slice(function_key_seq(n)?),
        _ => return None,
    }
    Some(out)
}

fn ctrl_byte(c: char) -> Option<u8> {
    let lc = c.to_ascii_lowercase();
    if lc.is_ascii_lowercase() {
        return Some((lc as u8) - b'a' + 1);
    }
    match c {
        '@' | ' ' => Some(0),
        '[' => Some(27),
        '\\' => Some(28),
        ']' => Some(29),
        '^' => Some(30),
        '_' => Some(31),
        '?' => Some(127),
        _ => None,
    }
}

fn function_key_seq(n: u8) -> Option<&'static [u8]> {
    Some(match n {
        1 => b"\x1bOP",
        2 => b"\x1bOQ",
        3 => b"\x1bOR",
        4 => b"\x1bOS",
        5 => b"\x1b[15~",
        6 => b"\x1b[17~",
        7 => b"\x1b[18~",
        8 => b"\x1b[19~",
        9 => b"\x1b[20~",
        10 => b"\x1b[21~",
        11 => b"\x1b[23~",
        12 => b"\x1b[24~",
        _ => return None,
    })
}

fn to_textarea_key(k: KeyEvent) -> tui_textarea::Input {
    let key = match k.code {
        KeyCode::Backspace => Key::Backspace,
        KeyCode::Enter => Key::Enter,
        KeyCode::Left => Key::Left,
        KeyCode::Right => Key::Right,
        KeyCode::Up => Key::Up,
        KeyCode::Down => Key::Down,
        KeyCode::Home => Key::Home,
        KeyCode::End => Key::End,
        KeyCode::PageUp => Key::PageUp,
        KeyCode::PageDown => Key::PageDown,
        KeyCode::Tab => Key::Tab,
        KeyCode::Delete => Key::Delete,
        KeyCode::Esc => Key::Esc,
        KeyCode::Char(c) => Key::Char(c),
        _ => Key::Null,
    };
    let mut input = Input {
        key,
        ctrl: k.modifiers.contains(KeyModifiers::CONTROL),
        alt: k.modifiers.contains(KeyModifiers::ALT),
        shift: k.modifiers.contains(KeyModifiers::SHIFT),
    };
    if k.modifiers.contains(KeyModifiers::SUPER) {
        input.alt = true;
    }
    input
}

fn edit_line_tail(line: &mut String, key: KeyEvent) {
    let mut cursor = line.len();
    edit_line(line, &mut cursor, key);
}

fn edit_line(line: &mut String, cursor: &mut usize, key: KeyEvent) {
    match key.code {
        KeyCode::Char(c) if !key.modifiers.contains(KeyModifiers::CONTROL) => {
            line.insert(*cursor, c);
            *cursor += c.len_utf8();
        }
        KeyCode::Backspace => {
            if *cursor > 0 {
                let prev = prev_char_boundary(line, *cursor);
                line.replace_range(prev..*cursor, "");
                *cursor = prev;
            }
        }
        KeyCode::Delete => {
            if *cursor < line.len() {
                let next = next_char_boundary(line, *cursor);
                line.replace_range(*cursor..next, "");
            }
        }
        KeyCode::Left => {
            if *cursor > 0 {
                *cursor = prev_char_boundary(line, *cursor);
            }
        }
        KeyCode::Right => {
            if *cursor < line.len() {
                *cursor = next_char_boundary(line, *cursor);
            }
        }
        KeyCode::Home => *cursor = 0,
        KeyCode::End => *cursor = line.len(),
        _ => {}
    }
}

fn prev_char_boundary(s: &str, idx: usize) -> usize {
    let mut i = idx.saturating_sub(1);
    while i > 0 && !s.is_char_boundary(i) {
        i -= 1;
    }
    i
}

fn next_char_boundary(s: &str, idx: usize) -> usize {
    let mut i = (idx + 1).min(s.len());
    while i < s.len() && !s.is_char_boundary(i) {
        i += 1;
    }
    i
}

#[cfg(test)]
mod tests {
    use super::*;

    fn key(code: KeyCode, mods: KeyModifiers) -> KeyEvent {
        KeyEvent::new(code, mods)
    }

    fn press(app: &mut App, code: KeyCode) {
        app.handle_key(key(code, KeyModifiers::NONE));
    }

    fn type_text(app: &mut App, text: &str) {
        for ch in text.chars() {
            press(app, KeyCode::Char(ch));
        }
    }

    fn set_backup_path(app: &mut App, path: &str) {
        if let Mode::Backup(state) = &mut app.mode {
            state.input = path.to_string();
        } else {
            panic!("la app no está en modo backup");
        }
    }

    /// Sends the app prefix (`Ctrl+Q`) followed by a command letter.
    fn prefix_cmd(app: &mut App, c: char) {
        app.handle_key(key(KeyCode::Char(PREFIX_KEY), KeyModifiers::CONTROL));
        app.handle_key(key(KeyCode::Char(c), KeyModifiers::NONE));
    }

    /// A store backed by a throwaway file so tests never touch real profiles.
    fn temp_store(tag: &str) -> SecureStore {
        let dir = std::env::temp_dir().join(format!("tuissh-store-{}-{tag}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        SecureStore::with_data_path(dir.join("profiles.enc"))
    }

    #[test]
    fn backup_export_flow_writes_encrypted_file() {
        let dir = std::env::temp_dir().join(format!("tuissh-app-export-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        let out = dir.join("backup.json");
        let out_str = out.to_string_lossy().to_string();

        let store = temp_store("export");
        let profiles = vec![ConnectionProfile {
            name: "prod".into(),
            host: "example.com".into(),
            port: 22,
            user: "root".into(),
            password: "topsecret".into(),
            ..Default::default()
        }];
        let mut app = App::new(store, profiles);

        // Ctrl+Q x starts the export, then fill in a known destination.
        prefix_cmd(&mut app, 'x');
        assert!(matches!(app.mode, Mode::Backup(_)));
        set_backup_path(&mut app, &out_str);
        press(&mut app, KeyCode::Enter); // accept path -> ask password
        type_text(&mut app, "pw123-long-enough");
        press(&mut app, KeyCode::Enter); // -> confirm
        type_text(&mut app, "pw123-long-enough");
        press(&mut app, KeyCode::Enter); // write file

        assert!(matches!(app.mode, Mode::List), "debería volver a la lista");
        let written = std::fs::read_to_string(&out).unwrap();
        assert!(!written.contains("topsecret"), "el backup filtró el password");
        assert!(!written.contains("example.com"), "el backup filtró el host");

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn backup_export_rejects_mismatched_confirmation() {
        let store = temp_store("mismatch");
        let mut app = App::new(store, vec![]);

        prefix_cmd(&mut app, 'x');
        set_backup_path(&mut app, "/tmp/does-not-matter.json");
        press(&mut app, KeyCode::Enter);
        type_text(&mut app, "aaaaaaaaaaaaaaaa");
        press(&mut app, KeyCode::Enter);
        type_text(&mut app, "bbbbbbbbbbbbbbbb");
        press(&mut app, KeyCode::Enter);

        // Still in the confirm step, showing an error, no file written.
        match &app.mode {
            Mode::Backup(state) => {
                assert!(state.error.is_some(), "debería avisar que no coinciden");
                assert_eq!(state.step, BackupStep::Confirm);
            }
            _ => panic!("debería seguir en el flujo de backup"),
        }
    }

    #[test]
    fn backup_import_wrong_passphrase_stays_in_flow() {
        let dir = std::env::temp_dir().join(format!("tuissh-app-import-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        let file = dir.join("backup.json");
        let file_str = file.to_string_lossy().to_string();

        let profiles = vec![ConnectionProfile {
            name: "prod".into(),
            host: "example.com".into(),
            port: 22,
            user: "root".into(),
            password: "topsecret".into(),
            ..Default::default()
        }];
        std::fs::write(
            &file,
            crate::store::export_backup(&profiles, "correct-passphrase").unwrap(),
        )
        .unwrap();

        let store = temp_store("wrongpass");
        let mut app = App::new(store, vec![]);

        // Ctrl+Q r starts the import.
        prefix_cmd(&mut app, 'r');
        set_backup_path(&mut app, &file_str);
        press(&mut app, KeyCode::Enter);
        type_text(&mut app, "wrong-passphrase");
        press(&mut app, KeyCode::Enter);

        match &app.mode {
            Mode::Backup(state) => {
                assert!(state.error.is_some(), "debería reportar contraseña incorrecta");
                assert_eq!(state.step, BackupStep::Pass);
            }
            _ => panic!("debería quedarse en el paso de contraseña"),
        }
        assert!(app.profiles.is_empty(), "no debió importar nada");

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn backup_import_updates_profiles() {
        let dir = std::env::temp_dir().join(format!("tuissh-app-import-ok-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        let file = dir.join("backup.json");
        let file_str = file.to_string_lossy().to_string();

        let profiles = vec![ConnectionProfile {
            name: "prod".into(),
            host: "example.com".into(),
            port: 22,
            user: "root".into(),
            password: "topsecret".into(),
            ..Default::default()
        }];
        std::fs::write(
            &file,
            crate::store::export_backup(&profiles, "correct-passphrase").unwrap(),
        )
        .unwrap();

        let store = temp_store("importok");
        let mut app = App::new(store, vec![]);

        prefix_cmd(&mut app, 'r');
        set_backup_path(&mut app, &file_str);
        press(&mut app, KeyCode::Enter);
        type_text(&mut app, "correct-passphrase");
        press(&mut app, KeyCode::Enter);

        assert_eq!(app.profiles.len(), 1);
        assert_eq!(app.profiles[0].password, "topsecret");
        assert_eq!(app.profiles[0].host, "example.com");

        let _ = std::fs::remove_dir_all(&dir);
    }

    fn host_key_info() -> HostKeyInfo {
        HostKeyInfo {
            host: "example.com".into(),
            port: 22,
            key_type: "ssh-ed25519".into(),
            fingerprint: "SHA256:abc".into(),
        }
    }

    #[test]
    fn unknown_host_key_opens_the_confirmation_prompt() {
        let mut app = App::new(temp_store("hk_open"), vec![]);
        app.connecting = true;
        app.mode = Mode::Session;
        app.apply_ssh_event(SshEvent::ConfirmHostKey(host_key_info()));
        assert!(matches!(app.mode, Mode::HostKey(_)));
    }

    #[test]
    fn host_key_needs_an_explicit_yes() {
        // Enter alone must not trust the key: it is only prompted, never defaulted.
        for (answer, expect_accept) in [("", false), ("y", false), ("no", false), ("yes", true)] {
            let mut app = App::new(temp_store("hk_answer"), vec![]);
            app.connecting = true;
            app.mode = Mode::Session;
            app.apply_ssh_event(SshEvent::ConfirmHostKey(host_key_info()));
            type_text(&mut app, answer);
            press(&mut app, KeyCode::Enter);
            assert!(matches!(app.mode, Mode::Session));
            assert_eq!(
                app.status.starts_with("Host key aceptada"),
                expect_accept,
                "respuesta {answer:?}"
            );
        }
    }

    #[test]
    fn esc_rejects_the_host_key_even_after_typing_yes() {
        let mut app = App::new(temp_store("hk_esc"), vec![]);
        app.connecting = true;
        app.mode = Mode::Session;
        app.apply_ssh_event(SshEvent::ConfirmHostKey(host_key_info()));
        type_text(&mut app, "yes");
        press(&mut app, KeyCode::Esc);
        assert!(app.status.starts_with("Host key rechazada"));
    }

    #[test]
    fn backup_export_rejects_short_passphrase() {
        let mut app = App::new(temp_store("shortpw"), vec![]);
        prefix_cmd(&mut app, 'x');
        set_backup_path(&mut app, "/tmp/does-not-matter.json");
        press(&mut app, KeyCode::Enter);
        type_text(&mut app, "corta");
        press(&mut app, KeyCode::Enter);
        match &app.mode {
            Mode::Backup(state) => {
                assert!(state.error.is_some());
                assert_eq!(state.step, BackupStep::Pass);
            }
            _ => panic!("debería seguir en el paso de contraseña"),
        }
    }

    #[test]
    fn encodes_plain_chars() {
        assert_eq!(
            encode_key(key(KeyCode::Char('a'), KeyModifiers::NONE)),
            Some(b"a".to_vec())
        );
    }

    #[test]
    fn encodes_ctrl_letters() {
        assert_eq!(
            encode_key(key(KeyCode::Char('c'), KeyModifiers::CONTROL)),
            Some(vec![3])
        );
        assert_eq!(
            encode_key(key(KeyCode::Char('d'), KeyModifiers::CONTROL)),
            Some(vec![4])
        );
    }

    #[test]
    fn encodes_special_keys() {
        assert_eq!(
            encode_key(key(KeyCode::Enter, KeyModifiers::NONE)),
            Some(vec![b'\r'])
        );
        assert_eq!(
            encode_key(key(KeyCode::Esc, KeyModifiers::NONE)),
            Some(vec![0x1b])
        );
        assert_eq!(
            encode_key(key(KeyCode::Up, KeyModifiers::NONE)),
            Some(b"\x1b[A".to_vec())
        );
        assert_eq!(
            encode_key(key(KeyCode::Backspace, KeyModifiers::NONE)),
            Some(vec![0x7f])
        );
    }

    #[test]
    fn prefers_arrow_keys_over_alt_prefix() {
        // Alt+char must send ESC prefix then the char.
        assert_eq!(
            encode_key(key(KeyCode::Char('b'), KeyModifiers::ALT)),
            Some(vec![0x1b, b'b'])
        );
    }

    #[test]
    fn edits_line_from_tail() {
        let mut s = "abc".to_string();
        let mut cur = s.len();
        edit_line(
            &mut s,
            &mut cur,
            key(KeyCode::Char('d'), KeyModifiers::NONE),
        );
        assert_eq!(s, "abcd");
    }

    // ---- Key model: every action goes through the Ctrl+Q prefix ----

    #[test]
    fn plain_letters_never_trigger_list_actions() {
        let store = temp_store("plain");
        let profiles = vec![ConnectionProfile {
            name: "prod".into(),
            host: "example.com".into(),
            port: 22,
            user: "root".into(),
            ..Default::default()
        }];
        let mut app = App::new(store, profiles);

        for c in ['n', 'e', 'd', 'x', 'r', 'q'] {
            app.handle_key(key(KeyCode::Char(c), KeyModifiers::NONE));
        }

        assert!(matches!(app.mode, Mode::List), "una letra abrió un modo");
        assert_eq!(app.profiles.len(), 1, "una letra borró un perfil");
        assert!(!app.should_quit, "una letra salió de la app");
    }

    #[test]
    fn prefix_then_letter_runs_list_action() {
        let store = temp_store("prefixn");
        let mut app = App::new(store, vec![]);

        prefix_cmd(&mut app, 'n');
        assert!(matches!(app.mode, Mode::Form(_)), "Ctrl+Q n no abrió el form");
    }

    #[test]
    fn ctrl_c_quits_from_list_as_emergency_exit() {
        let store = temp_store("ctrlc");
        let mut app = App::new(store, vec![]);
        app.handle_key(key(KeyCode::Char('c'), KeyModifiers::CONTROL));
        assert!(app.should_quit);
    }

    #[test]
    fn stray_prefix_expires_instead_of_swallowing_a_later_key() {
        let store = temp_store("expire");
        let mut app = App::new(store, vec![]);

        app.begin_prefix(LIST_HELP);
        assert!(app.prefix);
        app.prefix_at = Instant::now() - PREFIX_TIMEOUT - Duration::from_secs(1);
        app.expire_prefix();
        assert!(!app.prefix, "el prefijo debió caducar");
    }

    #[test]
    fn session_prefix_unknown_key_is_forwarded_not_swallowed() {
        let store = temp_store("sessunknown");
        let mut app = App::new(store, vec![]);
        app.mode = Mode::Session;
        app.connected = true;

        app.handle_key(key(KeyCode::Char('q'), KeyModifiers::CONTROL)); // prefix
        assert!(app.prefix);
        app.handle_key(key(KeyCode::Char('z'), KeyModifiers::NONE)); // unknown command

        assert!(
            matches!(app.mode, Mode::Session),
            "una tecla desconocida sacó de la sesión"
        );
        assert!(!app.prefix, "el prefijo debió consumirse");
    }

    #[test]
    fn session_prefix_then_ctrl_q_is_forwarded_not_a_command() {
        let store = temp_store("sessxoff");
        let mut app = App::new(store, vec![]);
        app.mode = Mode::Session;
        app.connected = true;

        app.handle_key(key(KeyCode::Char('q'), KeyModifiers::CONTROL)); // prefix
        app.handle_key(key(KeyCode::Char('q'), KeyModifiers::CONTROL)); // -> XON, not "disconnect"

        assert!(
            matches!(app.mode, Mode::Session),
            "Ctrl+Q tras el prefijo no debe desconectar"
        );
    }

    #[test]
    fn session_prefix_then_plain_d_is_forwarded_not_a_disconnect() {
        // Regression: typing `cd /var/www/...` must never end the session, even
        // if a stray prefix is armed right before the `d`.
        let store = temp_store("sessd");
        let mut app = App::new(store, vec![]);
        app.mode = Mode::Session;
        app.connected = true;

        app.handle_key(key(KeyCode::Char('q'), KeyModifiers::CONTROL)); // prefix
        app.handle_key(key(KeyCode::Char('d'), KeyModifiers::NONE));

        assert!(
            matches!(app.mode, Mode::Session),
            "una `d` tras el prefijo desconectó la sesión"
        );
        assert!(app.connected);
    }

    #[test]
    fn session_prefix_then_ctrl_d_disconnects() {
        let store = temp_store("sessctrld");
        let mut app = App::new(store, vec![]);
        app.mode = Mode::Session;
        app.connected = true;

        app.handle_key(key(KeyCode::Char('q'), KeyModifiers::CONTROL)); // prefix
        app.handle_key(key(KeyCode::Char('d'), KeyModifiers::CONTROL));

        assert!(matches!(app.mode, Mode::List), "Ctrl+Q Ctrl+D debe desconectar");
        assert!(!app.connected);
    }

    #[test]
    fn session_lost_returns_to_list_with_an_error() {
        let store = temp_store("sesslost");
        let mut app = App::new(store, vec![]);
        app.mode = Mode::Session;
        app.connected = true;

        app.on_session_lost("sesión finalizada: shell roto");

        assert!(matches!(app.mode, Mode::List), "no salió de la sesión muerta");
        assert!(!app.connected);
        assert!(app.error.is_some(), "no se reportó el error");
    }

    #[test]
    fn session_lost_error_survives_the_next_keystrokes() {
        let store = temp_store("sesslost_sticky");
        let mut app = App::new(store, vec![]);
        app.mode = Mode::Session;
        app.connected = true;

        app.on_session_lost("sesión finalizada: la conexión TCP se cerró");
        // The user keeps typing the command they were in the middle of; that
        // must not erase the explanation.
        app.handle_key(key(KeyCode::Char('c'), KeyModifiers::NONE));
        app.handle_key(key(KeyCode::Char('d'), KeyModifiers::NONE));

        assert!(
            app.error.is_some(),
            "el aviso de sesión perdida desapareció al seguir escribiendo"
        );
    }
}
