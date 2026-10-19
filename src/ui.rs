use ratatui::layout::{Constraint, Direction, Layout, Rect};
use ratatui::prelude::*;
use ratatui::widgets::{Block, Borders, List, ListItem, ListState, Paragraph, Wrap};

use crate::app::{App, BackupKind, BackupStep, Mode};

/// Rows the status bar needs: a long message wraps onto several lines, except
/// during a session where the PTY size is derived from a fixed-height bar.
fn status_height(app: &App, text: &str, width: u16) -> u16 {
    if matches!(app.mode, Mode::Session) || width == 0 {
        return 2;
    }
    let lines = text.chars().count().div_ceil(width as usize).clamp(1, 6) as u16;
    lines + 1
}

/// Inner size available to the remote PTY given the whole terminal size.
pub fn session_inner_size(term_cols: u16, term_rows: u16) -> (u16, u16) {
    let cols = term_cols.saturating_sub(38).saturating_sub(2).max(20);
    let rows = term_rows.saturating_sub(2).saturating_sub(2).max(5);
    (cols, rows)
}

pub fn draw(app: &mut App, f: &mut Frame<'_>) {
    let root = f.area();
    let status_text = if let Some(err) = &app.error {
        format!("ERROR: {err}")
    } else {
        app.status.clone()
    };
    let status_rows = status_height(app, &status_text, root.width);
    let layout = Layout::default()
        .direction(Direction::Vertical)
        .constraints([Constraint::Min(1), Constraint::Length(status_rows)])
        .split(root);

    let cols = Layout::default()
        .direction(Direction::Horizontal)
        .constraints([Constraint::Length(38), Constraint::Min(40)])
        .split(layout[0]);

    draw_profiles(app, f, cols[0]);
    draw_right_panel(app, f, cols[1]);

    let status = Paragraph::new(status_text)
        .style(if app.error.is_some() {
            Style::default().fg(Color::Red)
        } else {
            Style::default().fg(Color::Green)
        })
        .wrap(Wrap { trim: false })
        .block(Block::default().borders(Borders::TOP));
    f.render_widget(status, layout[1]);
}

fn spinner(tick: usize) -> char {
    const FRAMES: [char; 4] = ['|', '/', '-', '\\'];
    FRAMES[(tick / 2) % FRAMES.len()]
}

fn draw_profiles(app: &App, f: &mut Frame<'_>, area: Rect) {
    let items: Vec<ListItem> = app
        .profiles
        .iter()
        .map(|p| {
            ListItem::new(format!(
                "{}\n{}@{}:{} ({:?})",
                p.name, p.user, p.host, p.port, p.auth_method
            ))
        })
        .collect();
    let mut state = ListState::default();
    if !app.profiles.is_empty() {
        state.select(Some(app.selected.min(app.profiles.len() - 1)));
    }
    let list = List::new(items)
        .block(Block::default().title("Conexiones").borders(Borders::ALL))
        .highlight_style(Style::default().fg(Color::Black).bg(Color::Magenta))
        .highlight_symbol("▶ ");
    f.render_stateful_widget(list, area, &mut state);
}

fn draw_right_panel(app: &mut App, f: &mut Frame<'_>, area: Rect) {
    match &mut app.mode {
        Mode::List => {
            let text = "Selecciona una conexión a la izquierda y presiona Enter para conectar.\n\nTodas las acciones usan el prefijo Ctrl+Q:\n  Ctrl+Q n: nuevo perfil\n  Ctrl+Q e: editar perfil\n  Ctrl+Q d: borrar perfil\n  Ctrl+Q x: exportar backup cifrado\n  Ctrl+Q r: importar backup\n  Ctrl+Q q: salir\n\nCtrl+C: salir (emergencia)";
            let p = Paragraph::new(text)
                .block(Block::default().title("Consola").borders(Borders::ALL))
                .wrap(Wrap { trim: false });
            f.render_widget(p, area);
        }
        Mode::Session => {
            if app.connecting {
                let block = Block::default()
                    .title(format!("Sesión SSH {} conectando...", spinner(app.tick)))
                    .borders(Borders::ALL);
                let p = Paragraph::new("Estableciendo conexión...")
                    .block(block)
                    .wrap(Wrap { trim: false });
                f.render_widget(p, area);
                return;
            }
            let title = {
                let t = app.parser.screen().title().to_string();
                if t.is_empty() {
                    "Sesión SSH".to_string()
                } else {
                    format!("Sesión SSH — {t}")
                }
            };
            let block = Block::default().title(title).borders(Borders::ALL);
            let inner = block.inner(area);
            f.render_widget(block, area);
            render_terminal(app, f, inner);
        }
        Mode::HostKey(prompt) => {
            let info = &prompt.info;
            let lines = vec![
                Line::from(Span::styled(
                    "No se puede verificar la autenticidad de este host.",
                    Style::default().fg(Color::Yellow),
                )),
                Line::from(""),
                Line::from(format!("Host:          {}:{}", info.host, info.port)),
                Line::from(format!("Tipo de clave: {}", info.key_type)),
                Line::from(format!("Huella:        {}", info.fingerprint)),
                Line::from(""),
                Line::from(
                    "Compara la huella con la que te dio el administrador del servidor. \
                     Si no coincide, un tercero podría estar interceptando la conexión \
                     y robar tu contraseña.",
                ),
                Line::from(""),
                Line::from("Escribe «yes» y Enter para confiar y guardarla en known_hosts."),
                Line::from("Esc: cancelar (no se envía ninguna credencial)."),
                Line::from(""),
                Line::from(vec![
                    Span::styled("› ", Style::default().fg(Color::Magenta)),
                    Span::raw(prompt.input.clone()),
                ]),
            ];
            let p = Paragraph::new(lines)
                .block(Block::default().title("Host desconocido").borders(Borders::ALL))
                .wrap(Wrap { trim: false });
            f.render_widget(p, area);
        }
        Mode::Form(form) => {
            let lines: Vec<Line> = form
                .fields
                .iter()
                .enumerate()
                .map(|(idx, field)| {
                    let is_selected = idx == form.index;
                    let value = if field.secret {
                        "•".repeat(field.value.chars().count())
                    } else {
                        field.value.clone()
                    };
                    let prefix = if is_selected { "▶ " } else { "  " };
                    Line::from(vec![
                        Span::styled(
                            format!("{prefix}{}: ", field.label),
                            if is_selected {
                                Style::default().fg(Color::Magenta)
                            } else {
                                Style::default()
                            },
                        ),
                        Span::raw(value),
                    ])
                })
                .collect();
            let p = Paragraph::new(lines)
                .block(
                    Block::default()
                        .title("Editar perfil (Tab/Shift+Tab, Enter guardar, Esc cancelar)")
                        .borders(Borders::ALL),
                )
                .wrap(Wrap { trim: false });
            f.render_widget(p, area);
        }
        Mode::Prompt(prompt) => {
            let p = Paragraph::new(format!("{}\n\n{}", prompt.title, prompt.input))
                .block(Block::default().title("Acción").borders(Borders::ALL))
                .wrap(Wrap { trim: false });
            f.render_widget(p, area);
        }
        Mode::FileEdit => {
            if let Some(editor) = app.editor.as_mut() {
                let block = Block::default()
                    .title(format!(
                        "Editando remoto: {} (Ctrl+S guardar, Esc cancelar)",
                        app.edit_path
                    ))
                    .borders(Borders::ALL);
                editor.set_block(block);
                f.render_widget(&*editor, area);
            }
        }
        Mode::Backup(state) => {
            let secret = matches!(state.step, BackupStep::Pass | BackupStep::Confirm);
            let shown = if secret {
                "•".repeat(state.input.chars().count())
            } else {
                state.input.clone()
            };
            let title = match (state.kind, state.step) {
                (BackupKind::Export, BackupStep::Path) => "Exportar backup — destino",
                (BackupKind::Export, _) => "Exportar backup — contraseña",
                (BackupKind::Import, BackupStep::Path) => "Importar backup — archivo",
                (BackupKind::Import, _) => "Importar backup — contraseña",
            };
            let mut lines = vec![
                Line::from(state.message.clone()),
                Line::from(""),
                Line::from(vec![
                    Span::styled("› ", Style::default().fg(Color::Magenta)),
                    Span::raw(shown),
                ]),
            ];
            if let Some(err) = &state.error {
                lines.push(Line::from(""));
                lines.push(Line::from(Span::styled(
                    err.clone(),
                    Style::default().fg(Color::Red),
                )));
            }
            lines.push(Line::from(""));
            lines.push(Line::from(Span::styled(
                "Enter: continuar · Esc: cancelar",
                Style::default().fg(Color::DarkGray),
            )));
            let p = Paragraph::new(lines)
                .block(Block::default().title(title).borders(Borders::ALL))
                .wrap(Wrap { trim: false });
            f.render_widget(p, area);
        }
    }
}

/// Renders the VT screen buffer into the given area, applying colors and
/// attributes, and places the terminal cursor.
fn render_terminal(app: &mut App, f: &mut Frame<'_>, area: Rect) {
    let (rows, cols) = app.parser.screen().size();
    if rows != area.height || cols != area.width {
        app.parser.set_size(area.height.max(1), area.width.max(1));
    }
    let screen = app.parser.screen();
    let (rows, cols) = screen.size();

    let mut lines: Vec<Line> = Vec::with_capacity(rows as usize);
    for r in 0..rows {
        let mut spans: Vec<Span> = Vec::new();
        let mut run_style = Style::default();
        let mut run_text = String::new();
        for c in 0..cols {
            let cell = screen.cell(r, c);
            if cell.is_some_and(|c| c.is_wide_continuation()) {
                continue;
            }
            let style = cell.map_or_else(Style::default, cell_style);
            if style != run_style {
                if !run_text.is_empty() {
                    spans.push(Span::styled(std::mem::take(&mut run_text), run_style));
                }
                run_style = style;
            }
            match cell {
                Some(cell) if cell.has_contents() => run_text.push_str(&cell.contents()),
                _ => run_text.push(' '),
            }
        }
        if !run_text.is_empty() {
            spans.push(Span::styled(run_text, run_style));
        }
        lines.push(Line::from(spans));
    }
    f.render_widget(Paragraph::new(lines), area);

    if !screen.hide_cursor() {
        let (cr, cc) = screen.cursor_position();
        if cc < area.width && cr < area.height {
            f.set_cursor_position((area.x + cc, area.y + cr));
        }
    }
}

fn cell_style(cell: &vt100::Cell) -> Style {
    let mut fg = color_of(cell.fgcolor());
    let mut bg = color_of(cell.bgcolor());
    if cell.inverse() {
        std::mem::swap(&mut fg, &mut bg);
    }
    let mut style = Style::default().fg(fg).bg(bg);
    if cell.bold() {
        style = style.add_modifier(Modifier::BOLD);
    }
    if cell.italic() {
        style = style.add_modifier(Modifier::ITALIC);
    }
    if cell.underline() {
        style = style.add_modifier(Modifier::UNDERLINED);
    }
    style
}

fn color_of(c: vt100::Color) -> Color {
    match c {
        vt100::Color::Default => Color::Reset,
        vt100::Color::Idx(i) => Color::Indexed(i),
        vt100::Color::Rgb(r, g, b) => Color::Rgb(r, g, b),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::app::{App, BackupState, Mode};
    use crate::store::{ConnectionProfile, SecureStore};
    use ratatui::backend::TestBackend;
    use ratatui::Terminal;

    /// A store backed by a throwaway file so UI tests never touch real profiles.
    fn temp_store() -> SecureStore {
        let dir = std::env::temp_dir().join(format!("tuissh-ui-{}", std::process::id()));
        let _ = std::fs::create_dir_all(&dir);
        SecureStore::with_data_path(dir.join("profiles.enc"))
    }

    fn render_to_string(app: &mut App, w: u16, h: u16) -> String {
        let backend = TestBackend::new(w, h);
        let mut terminal = Terminal::new(backend).unwrap();
        terminal.draw(|f| draw(app, f)).unwrap();
        let buf = terminal.backend().buffer().clone();
        let area = buf.area;
        let mut out = String::new();
        for y in 0..area.height {
            for x in 0..area.width {
                out.push_str(buf.cell((x, y)).map(|c| c.symbol()).unwrap_or(" "));
            }
            out.push('\n');
        }
        out
    }

    #[test]
    fn renders_the_profile_panel() {
        let store = temp_store();
        let profiles = vec![ConnectionProfile {
            name: "prod".into(),
            host: "example.com".into(),
            port: 22,
            user: "root".into(),
            ..Default::default()
        }];
        let mut app = App::new(store, profiles);
        let s = render_to_string(&mut app, 100, 30);
        assert!(
            s.contains("Conexiones"),
            "falta el panel de conexiones:\n{s}"
        );
        assert!(s.contains("prod"), "falta el perfil listado:\n{s}");
    }

    #[test]
    fn renders_terminal_screen_contents() {
        let store = temp_store();
        let mut app = App::new(store, vec![]);
        app.connecting = false;
        app.connected = true;
        app.mode = Mode::Session;
        app.parser.set_size(20, 60);
        app.parser
            .process(b"hola terminal \x1b[1;31mROJO\x1b[0m\r\n$ ");
        let s = render_to_string(&mut app, 100, 30);
        assert!(
            s.contains("hola terminal"),
            "falta el texto del terminal:\n{s}"
        );
        assert!(s.contains("ROJO"), "falta el texto coloreado:\n{s}");
    }

    #[test]
    fn renders_backup_passphrase_prompt_masked() {
        let store = temp_store();
        let mut app = App::new(store, vec![]);
        app.mode = Mode::Backup(BackupState {
            kind: BackupKind::Export,
            step: BackupStep::Pass,
            path: "/tmp/backup.json".into(),
            pass: String::new(),
            input: "secreto".into(),
            message: "Contraseña para proteger el backup".into(),
            error: None,
        });

        let s = render_to_string(&mut app, 100, 30);
        assert!(s.contains("Exportar backup"), "falta el título:\n{s}");
        assert!(
            !s.contains("secreto"),
            "la contraseña no debe mostrarse en claro:\n{s}"
        );
        assert!(s.contains('•'), "la contraseña debe enmascararse:\n{s}");
    }

    #[test]
    fn renders_backup_path_prompt_in_clear() {
        let store = temp_store();
        let mut app = App::new(store, vec![]);
        app.mode = Mode::Backup(BackupState {
            kind: BackupKind::Import,
            step: BackupStep::Path,
            path: String::new(),
            pass: String::new(),
            input: "/tmp/mi-backup.json".into(),
            message: "Ruta del archivo de backup a importar".into(),
            error: None,
        });

        let s = render_to_string(&mut app, 100, 30);
        assert!(s.contains("Importar backup"), "falta el título:\n{s}");
        assert!(s.contains("/tmp/mi-backup.json"), "falta la ruta:\n{s}");
    }

    #[test]
    fn renders_host_key_prompt_with_fingerprint() {
        let store = temp_store();
        let mut app = App::new(store, vec![]);
        app.mode = Mode::HostKey(crate::app::HostKeyPrompt {
            info: crate::ssh::HostKeyInfo {
                host: "example.com".into(),
                port: 22,
                key_type: "ssh-ed25519".into(),
                fingerprint: "SHA256:abcdef".into(),
            },
            input: String::new(),
        });
        let s = render_to_string(&mut app, 120, 30);
        assert!(s.contains("Host desconocido"), "falta el título:\n{s}");
        assert!(s.contains("SHA256:abcdef"), "falta la huella:\n{s}");
        assert!(s.contains("ssh-ed25519"), "falta el tipo de clave:\n{s}");
    }
}
