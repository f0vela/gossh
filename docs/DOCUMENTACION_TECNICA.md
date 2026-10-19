# tuiSSH — Documentación técnica

Cliente SSH con interfaz de terminal (TUI) escrito en Rust. Gestiona perfiles de conexión cifrados en disco y ofrece una terminal remota interactiva con emulación VT, subida de archivos y edición remota por SFTP.

- Crate: `tuissh` 0.1.0 (edición 2021), binario único.
- Tamaño: ~3.300 líneas en 5 módulos (`app` 1530, `ssh` 849, `store` 495, `ui` 384, `main` 16), incluyendo tests.
- Idioma de la interfaz y de los mensajes de error: español.

---

## 1. Dependencias

| Crate | Uso |
|---|---|
| `ratatui` 0.29, `crossterm` 0.28 | Renderizado TUI, modo raw, pantalla alternativa, eventos de teclado/resize |
| `tui-textarea` 0.7 | Editor multilínea para archivos remotos |
| `vt100` 0.15 | Emulación del terminal remoto (pantalla, colores, cursor, título) |
| `ssh2` 0.9 | Bindings de libssh2: sesión, auth, PTY, SFTP, known_hosts |
| `aes-gcm` 0.10 | Cifrado AES-256-GCM (almacén y backups) |
| `argon2` 0.6 | KDF Argon2id para backups protegidos con contraseña |
| `keyring` 2.3 | Clave maestra en el keychain del sistema |
| `rand` 0.8 | Claves, nonces y salts (`OsRng`) |
| `serde`, `serde_json`, `base64` | Serialización y codificación |
| `dirs` 5.0 | Directorio de configuración y home |
| `anyhow` | Manejo de errores |

---

## 2. Arquitectura general

```
┌────────────────────────── Hilo principal (UI) ──────────────────────────┐
│  main.rs ─► App::run()                                                  │
│    event_loop (cada ≤50 ms):                                            │
│      1. drain_ssh_events()   ◄── SshEvent ──┐                           │
│      2. expire_prefix()                     │                           │
│      3. terminal.draw(ui::draw)             │   std::sync::mpsc         │
│      4. event::poll(50ms) → handle_key /    │                           │
│                              handle_resize ─┼── SshRequest ──┐         │
└─────────────────────────────────────────────┘                 ▼         │
                                         ┌────── Hilo worker SSH ───────┐ │
                                         │ spawn_ssh_worker()           │ │
                                         │ posee Option<SSHClient>      │ │
                                         │ (sesión, SFTP, canal shell)  │ │
                                         └──────────────────────────────┘ │
```

| Módulo | Responsabilidad |
|---|---|
| `main.rs` | Crea `SecureStore`, carga perfiles, construye `App` y llama a `run()`. |
| `store.rs` | Modelo `ConnectionProfile`, cifrado del almacén, clave en keychain, export/import de backups, permisos de archivos. |
| `ssh.rs` | `SSHClient` (conexión, auth, verificación de host key, PTY, SFTP), protocolo `SshRequest`/`SshEvent` y el bucle del worker. |
| `app.rs` | Estado global (`App`), máquina de modos, manejo de teclado, flujos de formulario/backup/prompt, codificación de teclas a bytes de PTY. |
| `ui.rs` | Layout, widgets ratatui y volcado del buffer `vt100` a la pantalla. |

Principio de diseño: **todo el estado de red vive en el worker**. La UI nunca hace E/S de red; solo envía mensajes y consume eventos, por lo que no se congela al conectar o transferir archivos.

---

## 3. Modelo de datos (`store.rs`)

```rust
enum AuthMethod { Password, Key, Both, Interactive }   // serde: minúsculas

struct ConnectionProfile {
    name, host: String, port: u16, user: String,
    auth_method: AuthMethod,      // #[serde(default)] → Password
    password: String,             // #[serde(default)]
    private_key_path: String,     // #[serde(default)]
    private_key_pass: String,     // #[serde(default)]
}
```

Los campos con `#[serde(default)]` permiten leer perfiles guardados por versiones anteriores sin esos campos.

### 3.1 Almacén local (`SecureStore`)

- Ruta: `<config_dir>/tuissh/profiles.enc` (`~/Library/Application Support/tuissh/` en macOS, `~/.config/tuissh/` en Linux). El directorio se crea con permisos `0700` y el archivo con `0600` (solo Unix).
- Contenido: `nonce (12 B) ‖ ciphertext+tag` de `AES-256-GCM` sobre el JSON (`to_vec_pretty`) de `Vec<ConnectionProfile>`. Nonce aleatorio nuevo por cada guardado.
- Clave maestra: 32 bytes aleatorios, guardados en base64 en el keychain (servicio `tuissh`, usuario `data-key`). Se crea en el primer uso.
- `save_profiles` reescribe el archivo completo en cada cambio, de forma atómica (archivo temporal `profiles.enc.tmp` + `rename`) (crear, editar, borrar, importar).

### 3.2 Resolución de la clave (`get_or_create_key`)

| Situación en keychain | Resultado |
|---|---|
| Entrada válida | Se usa. |
| `NoEntry` | Se genera y guarda una clave nueva. |
| Entrada con formato inválido y **sin** datos cifrados | Se regenera. |
| Entrada con formato inválido y **con** datos cifrados | Error explicativo (no se pisa la clave). |
| Otro error del keychain | Error `error leyendo keychain`. |

`decode_key` es deliberadamente tolerante: acepta base64 estándar / URL-safe (con y sin padding), hex de 64 caracteres (con o sin `0x`), 32 bytes crudos, un objeto JSON con claves `key|value|secret|password|data`, o tokens separados por `:`, `;` o espacios (formatos heredados).

### 3.3 Recuperación ante almacén ilegible

Si el descifrado falla en `load_profiles` (clave del keychain distinta o archivo corrupto), el archivo **no se borra**: se renombra a `profiles.enc.unreadable.<timestamp>.bak` y la app arranca con la lista vacía. `load_profiles` devuelve `LoadedProfiles { profiles, notice }`; `main` pasa el `notice` a `App::set_startup_notice`, que muestra durante 45 s un mensaje con la ruta del `.bak` y cómo recuperarse. La barra de estado se ajusta (hasta 6 líneas) para que el mensaje entre completo.

### 3.4 Backups portátiles

`export_backup(profiles, passphrase) -> String` produce un JSON:

```json
{
  "format": "tuissh-backup",
  "version": 2,
  "kdf": "argon2id",
  "m_cost": 65536,
  "t_cost": 3,
  "p_cost": 1,
  "salt": "<base64, 16 B>",
  "data": "<base64 de nonce‖ciphertext>",
  "created_at": 1700000000,
  "profiles_count": 3
}
```

- Clave: `Argon2id` v0x13 con **64 MiB, 3 pasadas, 1 carril** (guardados en el sobre como `m_cost/t_cost/p_cost`) y salt aleatorio de 16 bytes → 32 bytes.
- **Contraseña mínima de 12 caracteres** al exportar (validada en la UI y en `export_backup`). No se exige al importar, para poder abrir backups antiguos.
- **Compatibilidad:** `import_backup` acepta v1 (costes fijos 19 MiB/2 pasadas, sin campos de coste) y v2. En v2 los costes se validan contra topes (≤512 MiB, ≤10 pasadas, ≤8 carriles) para que un archivo manipulado no agote memoria ni CPU.
- Cifrado: el mismo `encrypt` (AES-256-GCM) del almacén; los perfiles incluyen contraseñas.
- `import_backup` valida `format` y `version`, longitud del salt y descifra; contraseña errónea o archivo manipulado → `contraseña incorrecta o backup corrupto` (el tag GCM detecta ambos).
- El archivo exportado se crea con `0600` desde el inicio (`write_private`).
- Nota: `kdf`, `created_at` y `profiles_count` son informativos; `import_backup` no usa `kdf` (siempre asume Argon2id).
- `write_private(path, data)`: crea el archivo con modo `0600` en la propia llamada `open` (sin ventana de lectura bajo la umask) y lo usan el almacén, los backups y el log.

---

## 4. Capa SSH (`ssh.rs`)

### 4.1 Conexión (`SSHClient::connect`)

1. `TcpStream::connect(host:port)` con timeouts de lectura/escritura de 20 s.
2. `try_clone()` del socket → `probe`, usado solo para detectar cierre del peer.
3. `Session::handshake()`.
4. `verify_host_key` (ver 4.3).
5. `authenticate` (ver 4.2).
6. Inicializa SFTP y calcula `home` con `realpath(".")` (fallback `/`).
7. `start_shell`: PTY `xterm-256color` del tamaño dado + `shell()`.
8. Pasa la sesión a **modo no bloqueante** y activa keepalive (`set_keepalive(true, 20)`).

**Modos de PTY:** `IXON`, `IXANY` e `IXOFF` desactivados para que un `Ctrl+S` accidental no congele la salida (y porque `Ctrl+Q`, tecla XON, es el prefijo de la app). Si el servidor rechaza los modos, se reintenta con los valores por defecto.

### 4.2 Autenticación

El orden de intentos depende de `auth_method`; se prueba cada uno hasta que uno autentique, y se informa el último error:

| `auth_method` | Intentos en orden |
|---|---|
| `Password` | password → keyboard-interactive |
| `Interactive` | keyboard-interactive → password |
| `Key` | clave privada |
| `Both` | clave privada → password → keyboard-interactive |

- Clave privada: `userauth_pubkey_file` con `~` expandido y passphrase opcional.
- Keyboard-interactive (`PasswordPrompter`): responde con el password guardado a todo prompt sin eco y con cadena vacía a los que tienen eco. No soporta segundos factores (OTP) interactivos.
- Un intento sin credencial configurada falla de inmediato con mensaje descriptivo y se pasa al siguiente.

### 4.3 Verificación de host key

Contra `~/.ssh/known_hosts` (formato OpenSSH, vía `ssh2::KnownHosts`):

| Resultado | Acción |
|---|---|
| `Match` | Continúa. |
| `Mismatch` | Aborta con aviso de posible MITM e incluye la huella `SHA256:…`. |
| `Failure` | Aborta. |
| `NotFound` | **Confirmación del usuario**: el worker emite `ConfirmHostKey` con host, tipo de clave y huella SHA256 y se bloquea hasta recibir `HostKeyDecision`. Solo si se acepta se añade la línea al final de `known_hosts` (sin reescribir el archivo). Rechaza tipos de clave desconocidos. |

Los hosts con puerto distinto de 22 se escriben como `[host]:puerto`. Si `known_hosts` existe pero no se puede abrir, la conexión se aborta (falla cerrado). Los errores al *parsear* líneas ajenas se ignoran: esos hosts aparecen como desconocidos y pasan por la confirmación. La verificación se hace **antes** de autenticar, así que ante un rechazo no se envía ninguna credencial. Como la comparación no fija el tipo de clave, una clave de otro tipo para un host conocido da `Mismatch` (no hay degradación silenciosa).

### 4.4 Operaciones SFTP

`upload_file`, `read_remote_file` y `write_remote_file` ponen temporalmente la sesión en modo **bloqueante**, ejecutan y vuelven a no bloqueante.

- `resolve_remote_path`: `""`/`.`/`~` → home; `~/x` → `home/x`; absoluta → normalizada; relativa → relativa a home. `normalize_posix_path` resuelve `.` y `..`.
- Subida y escritura crean los directorios padre (`mkdir_all`, modo `0755`, errores de `mkdir` ignorados) y truncan/crean el archivo destino. La subida lee el archivo local completo en memoria.
- Lectura usa `read_to_string`: falla con archivos que no sean UTF-8 válido.

### 4.5 Protocolo worker ↔ UI

```rust
enum SshRequest {            // UI → worker
    Connect { profile, cols, rows },
    Input(Vec<u8>), Resize(u16, u16),
    Upload { local, remote }, Read(String), Write { path, content },
    HostKeyDecision(bool),            // respuesta a ConfirmHostKey
    Disconnect, Shutdown,
}

enum SshEvent {              // worker → UI
    ConfirmHostKey(HostKeyInfo),
    Connected { home }, ConnectFailed(String),
    Output(Vec<u8>), ShellClosed,
    UploadDone { remote }, FileContent { path, content }, FileWritten { path },
    Failed { context, message },
    Disconnected, SessionLost { message },
}
```

### 4.6 Bucle del worker (`spawn_ssh_worker`)

Un único hilo, dos estados:

- **Sin cliente:** bloquea en `rx.recv()`. `Connect` intenta conectar y emite `Connected`/`ConnectFailed`; `Shutdown` o canal cerrado termina el hilo; el resto se ignora.
- **Con cliente:** cada iteración (con `sleep` de 8 ms):
  1. Vacía las peticiones pendientes con `try_recv` (entrada → buffer `pending`, resize, upload/read/write, disconnect/shutdown).
  2. Vuelca `pending` con `try_write` (no bloqueante; devuelve 0 si el canal está lleno y reintenta en la siguiente vuelta).
  3. Lee la salida del shell (`poll_shell`) y la emite como `Output`.
  4. Si la lectura está ociosa, consulta `peer_closed()` (`peek` sobre el socket duplicado).
  5. Cada 1 s envía `keepalive_send()`.

**Detección de caída de sesión** (emite `SessionLost` y limpia el cliente):

- Error de lectura del shell.
- Error duro de escritura.
- `pending` supera `MAX_PENDING_INPUT` (64 KiB): el remoto no está leyendo.
- El peer cerró el TCP (`peek` devuelve 0 o error distinto de `WouldBlock`).

EOF del canal emite `ShellClosed` (cierre normal, p. ej. `exit`). `Disconnect` emite `Disconnected`.

**Decisiones técnicas relevantes:**

- Nunca se llama a `Channel::flush()`: en libssh2 descarta datos entrantes del canal y se perdería el eco remoto.
- El `probe` TCP existe porque libssh2 puede seguir informando lecturas ociosas con el enlace ya muerto, lo que antes provocaba que se "tragaran" las teclas.
- `resize` cambia momentáneamente a modo bloqueante para `request_pty_size`.

### 4.7 Diagnóstico

Si existe la variable de entorno `TUISSH_LOG`, `debug_log` añade líneas `"<ms epoch> <mensaje>"` a ese archivo (eventos del worker y de la app, salvo `Output`). Sin la variable no hace nada.

---

## 5. Capa de aplicación (`app.rs`)

### 5.1 Máquina de modos

```rust
enum Mode { List, HostKey(HostKeyPrompt), Form(ProfileForm), Session, Prompt(PromptState), FileEdit, Backup(BackupState) }
```

```
                    Enter                     Ctrl+Q u / r
         ┌────────► Session ◄──────────────────────────────┐
         │             │  ▲                                │
         │  Ctrl+Q     │  │ Esc / FileWritten              ▼
         │  Ctrl+D /   │  └────────── FileEdit ◄──── Prompt (Upload | OpenRemote)
         │  SessionLost│                 ▲  FileContent
  List ◄─┴─────────────┘                 │
   │ Ctrl+Q n/e ──► Form ──(Enter/Esc)──► List
   └ Ctrl+Q x/r ──► Backup ─(fin/Esc)───► List
```

`Mode::Session` se entra al pulsar Enter (con `connecting = true`) y pasa a `connected = true` al recibir `Connected`. Si la conexión falla, `ConnectFailed` vuelve a `List` con error.

### 5.2 Modelo de teclas

Un único prefijo, **`Ctrl+Q`**, para toda acción de la app; ninguna letra suelta ni otra combinación `Ctrl+letra` dispara acciones.

| Contexto | Teclas |
|---|---|
| Lista | `↑`/`↓` mover · `Enter` conectar · `Ctrl+C` salir (emergencia) |
| Lista, tras `Ctrl+Q` | `n` nuevo · `e` editar · `d` borrar · `x` exportar · `r` importar · `q` salir · `Esc` cancela |
| Sesión | Todas las teclas van al shell remoto |
| Sesión, tras `Ctrl+Q` | `Ctrl+D` desconectar · `u` subir archivo · `r`/`o` abrir remoto · `?` ayuda · `Esc` cancela |
| Editor remoto | `Ctrl+S` guardar · `Esc` cancelar |
| Formulario | `Tab`/`Shift+Tab` mover · `Enter` guardar · `Esc` cancelar |

Salvaguardas:

- **Desconectar exige `Ctrl` en la segunda tecla**: una `d` suelta tras un prefijo accidental (p. ej. en `cd /var/www`) no cierra la sesión.
- **Tecla desconocida tras el prefijo en sesión → se reenvía al remoto**, no se descarta (incluye el `Ctrl+Q` doble, que llega como XON).
- El prefijo **caduca a los 4 s** (`PREFIX_TIMEOUT`).
- Mientras `connecting`, solo `Ctrl+C`/`Ctrl+Q` salen de la app.
- Los errores permanecen visibles al menos 6 s (`ERROR_MIN_DISPLAY`), para que las teclas que el usuario sigue pulsando tras una caída no los borren.

### 5.3 Formulario de perfil

Campos: nombre, host, puerto, usuario, auth (`password|key|both|interactive`; `kbi` es alias), password, ruta de clave, passphrase. Validaciones en `build_profile`: nombre/host/usuario obligatorios; puerto numérico `u16`; password requerido para `password`/`interactive`; ruta de clave requerida para `key`/`both`. Los campos secretos se muestran enmascarados (`•`). La edición de texto es siempre al final de la línea (no hay cursor intermedio en formularios).

### 5.4 Flujo de backup

`BackupState { kind: Export|Import, step: Path → Pass → Confirm }`:

- **Exportar:** ruta (por defecto `~/tuissh-backup-<epoch>.json`) → contraseña → confirmación (si no coincide, se limpia el campo y se queda en el paso) → escribe el archivo con `0600`.
- **Importar:** ruta (debe existir) → contraseña. Si es incorrecta, permanece en el paso de contraseña para reintentar. Se **añaden** perfiles nuevos y se **omiten** duplicados (mismo `name+host+port+user`); no se borra nada.
- `~/` se expande en las rutas.

### 5.5 Terminal remota

- `App.parser` es un `vt100::Parser` (sin scrollback: `scrollback_len = 0`). Cada `SshEvent::Output` se procesa con `parser.process`.
- El tamaño interior de la sesión se calcula con `ui::session_inner_size`: ancho − 38 (panel de perfiles) − 2 (bordes), mínimo 20; alto − 2 (barra de estado) − 2 (bordes), mínimo 5.
- `Event::Resize` actualiza el parser y envía `SshRequest::Resize`.
- `encode_key` traduce `KeyEvent` a bytes de terminal: caracteres UTF-8, `Ctrl+letra` (0x01–0x1A y `@ [ \ ] ^ _ ?`), `Alt` como prefijo `ESC`, flechas/Home/End/PgUp/PgDn/Ins/Del como secuencias CSI, `F1–F12` (`SS3` para F1–F4, `CSI n~` para el resto), `Shift+Tab` como `ESC [ Z`. No hay modo de teclas de aplicación (las flechas siempre se envían en modo normal `ESC [ A`).

### 5.6 Editor remoto

`Ctrl+Q r` → prompt de ruta → `SshRequest::Read` → `FileContent` abre un `TextArea` con el contenido. `Ctrl+S` envía `SshRequest::Write` con las líneas unidas por `\n`; al recibir `FileWritten` vuelve a la sesión. Nota: al guardar se reescribe el contenido con `\n` como separador y sin salto de línea final añadido.

---

## 6. Interfaz (`ui.rs`)

Layout: columna vertical (contenido + barra de estado de 2 líneas con borde superior); el contenido se divide horizontalmente en **panel de conexiones** (ancho fijo 38) y **panel derecho** (mín. 40).

El panel derecho depende del modo: ayuda en lista, terminal en sesión, formulario, prompt, editor o asistente de backup. La barra de estado muestra `ERROR: …` en rojo si hay error; si no, el estado en verde. Durante la conexión se muestra un spinner (`| / - \`) animado con el contador `tick`.

`render_terminal` recorre el buffer del parser celda a celda, agrupa tramos de igual estilo en `Span`s, omite continuaciones de caracteres anchos, aplica color (default/indexado/RGB), negrita, cursiva, subrayado e inverso, y coloca el cursor del terminal si no está oculto. Si el área no coincide con el tamaño del parser, lo redimensiona antes de pintar.

---

## 7. Flujos principales

**Conectar**

1. Enter en la lista → `Mode::Session`, `connecting = true`, parser nuevo, `SshRequest::Connect`.
2. El worker conecta (handshake → host key → auth → SFTP → PTY) y responde `Connected { home }`.
3. La UI fija `connected = true`, recrea el parser con el tamaño real, envía `Resize` y muestra el home en la barra de estado.
4. Entrada: tecla → `encode_key` → `SshRequest::Input` → buffer `pending` → `try_write`.
5. Salida: `poll_shell` → `SshEvent::Output` → `vt100` → `render_terminal`.

**Terminar:** `Ctrl+Q Ctrl+D` (`Disconnected`), `exit` remoto (`ShellClosed`), o caída (`SessionLost` con error visible).

**Salir de la app:** `run()` restaura el terminal (modo raw y pantalla alternativa) y envía `Shutdown` al worker. Esto ocurre también si el bucle devuelve error.

---

## 8. Seguridad

| Aspecto | Implementación |
|---|---|
| Datos en reposo | AES-256-GCM, nonce aleatorio por escritura, clave en keychain del SO |
| Permisos | Directorio `0700`, almacén y backups `0600` (solo Unix) |
| Backups | Argon2id + AES-256-GCM, salt aleatorio por backup, contraseña nunca persistida |
| Host keys | `known_hosts` con rechazo ante cambio y confirmación explícita (`yes`) de la huella para hosts nuevos |
| Teclas destructivas | Prefijo único; desconexión con `Ctrl` obligatorio |
| Secretos en UI | Passwords y passphrases enmascarados en formularios y backups |

Limitaciones conocidas: los secretos viven en memoria como `String` sin borrado seguro (`zeroize`); los perfiles (con contraseñas en claro dentro del JSON cifrado) se descifran por completo en memoria; el log de depuración no registra contenido de sesión pero sí hosts y usuarios.

---

## 9. Tests

`cargo test`. Los tests nunca tocan perfiles reales: usan `SecureStore::with_data_path` (solo `#[cfg(test)]`) con directorios temporales.

| Módulo | Cobertura |
|---|---|
| `store` | Roundtrip de cifrado, clave incorrecta, ciphertext truncado, decodificación de clave base64, roundtrip de backup (memoria y disco), contraseña incorrecta/vacía, archivos ajenos/corruptos, ciphertext sin texto plano |
| `ssh` | `normalize_posix_path`, campo `known_hosts` con puerto personalizado |
| `app` | Confirmación de host key (`yes` explícito, Esc rechaza), contraseña de backup corta, flujos de export/import (incluye confirmación distinta y contraseña errónea), codificación de teclas, edición de línea, modelo de prefijo (letras sueltas inertes, caducidad, reenvío de teclas desconocidas, `Ctrl+Q Ctrl+D`, regresión de `cd`), pérdida de sesión y persistencia del error |
| `ui` | Panel de perfiles, contenido del terminal con color, enmascarado de contraseña de backup y ruta en claro (con `TestBackend`) |

No hay tests de integración contra un servidor SSH real ni del bucle del worker.

---

## 10. Observaciones y deuda técnica

Detectadas durante la revisión del código; ordenadas por impacto aproximado. Ya resueltos: almacén ilegible sin aviso, TOFU sin confirmación, contraseña de backup débil, permisos tardíos y `known_hosts` ilegible (fallaba en abierto). Pendientes de la revisión de seguridad: sin timeout/cancelación al conectar, permisos `0644` y escritura no atómica en SFTP, título remoto sin filtrar.

1. **Operaciones SFTP bloquean el worker.** Mientras dura una subida/lectura/escritura (modo bloqueante, archivo entero en memoria) no se lee el shell ni se envían keepalives. Con archivos grandes o enlaces lentos puede disparar timeouts del servidor.
2. **Sin scrollback** (`vt100::Parser::new(rows, cols, 0)`): no se puede revisar salida que ya salió de pantalla.
3. **Teclas de aplicación no soportadas.** Las flechas siempre usan `ESC [ X`; programas que activan el modo de cursor de aplicación (DECCKM) pueden esperar `ESC O X`. Tampoco se maneja ratón, pegado en bloque (bracketed paste) ni `Ctrl`/`Shift` con flechas.
4. **Edición de texto limitada en formularios/prompts:** se escribe siempre al final (el cursor interno es una variable local que se reinicia cada tecla), aunque `edit_line` soporta posición.
5. **Guardado del editor remoto** une las líneas con `\n` sin salto final y descarta un fin de línea `\r\n` original; puede alterar archivos CRLF o sin newline final.
6. **Sin zeroize** de secretos en memoria (ver §8).
7. **README desactualizado:** la sección de funcionalidades menciona `Ctrl+X`/`Ctrl+R` para exportar/importar; el modelo vigente es `Ctrl+Q x` / `Ctrl+Q r`.

---

## 11. Guía rápida para desarrolladores

```bash
cargo run                                   # ejecutar
cargo build --release                       # binario optimizado: target/release/tuissh (LTO, strip)
cargo test                                  # tests
TUISSH_LOG=/tmp/tuissh.log cargo run        # con log de diagnóstico
```

**Añadir una acción de lista:** agregar la rama en `handle_list_keys` (dentro del bloque `if self.prefix`), actualizar `LIST_HELP` y el texto de ayuda en `ui::draw_right_panel`.

**Añadir una operación remota:** nueva variante en `SshRequest`/`SshEvent`, método en `SSHClient`, rama en el bucle del worker, manejo en `apply_ssh_event` y nombre en `ssh_event_name`.

**Añadir un campo de perfil:** campo en `ConnectionProfile` con `#[serde(default)]` (compatibilidad con datos existentes), entrada en `ProfileForm::from_profile`, validación en `build_profile` y uso en `ssh.rs`. Considerar si debe entrar en la comparación de duplicados de `finish_import`.

**Cambiar el formato de backup:** incrementar `BACKUP_VERSION`; `import_backup` rechaza versiones distintas, así que habrá que añadir ramas de compatibilidad.
