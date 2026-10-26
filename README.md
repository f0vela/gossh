# tuiSSH (Rust + ratatui)

App TUI en **Rust** para gestionar conexiones SSH desde terminal con almacenamiento seguro local.

## Funcionalidades

- Layout TUI de 2 columnas:
  - Izquierda: conexiones/perfiles siempre visibles.
  - Derecha: terminal SSH interactiva y flujos de acción (prompts, edición remota).
- Crear, editar, borrar y listar perfiles de conexión SSH.
- Guardado cifrado en disco (`AES-256-GCM`) y clave maestra en keychain del sistema.
- **SSH en un hilo de trabajo**: la interfaz nunca se congela mientras conecta,
  ejecuta comandos o transfiere archivos (spinner de progreso).
- Autenticación por password, private key (con/sin passphrase), keyboard-interactive
  (necesario en servidores con PAM/2FA), o combinaciones (`both`).
- **Terminal interactiva real**: abre un PTY con `xterm-256color` y emula la
  pantalla (colores ANSI de 256/truecolor, atributos, cursor). Puedes usar
  `vim`, `htop`, `tmux`, `sudo`, prompts interactivos, etc.
- **Control de flujo desactivado** en el PTY remoto (`IXON`/`IXANY`/`IXOFF`): un
  `Ctrl+S` accidental ya no puede suspender la salida y dejar la sesión
  aparentemente congelada.
- **Detección de caída de sesión**: si el shell remoto falla (red, lectura/escritura),
  la app sale de la sesión y avisa, en vez de quedarse en una terminal muerta
  donde lo que escribes no llega a ningún sitio.
- Redimensionado del PTY remoto al cambiar el tamaño de la ventana.
- Subida de archivos y lectura/escritura remota por SFTP.
- Editor remoto integrado con `Ctrl+S` para guardar.
- **Backup portátil de las conexiones**: exporta (`Ctrl+Q x`) todos los perfiles a un
  único archivo **cifrado con contraseña** (Argon2id + AES-256-GCM), incluyendo las
  contraseñas guardadas, e impórtalos (`Ctrl+Q r`) en otra máquina. Al importar, los
  perfiles nuevos se añaden y los duplicados se omiten.

## Arquitectura

| Módulo | Responsabilidad |
|---|---|
| `src/main.rs` | Arranque: crea el store y la app. |
| `src/store.rs` | Perfiles, cifrado AES-256-GCM, clave en keychain, permisos. |
| `src/ssh.rs` | Cliente SSH/SFTP, autenticación, host key, PTY interactivo y **worker thread**. |
| `src/app.rs` | Estado, máquina de estados, manejo de teclado y encoding al PTY. |
| `src/ui.rs` | Renderizado (ratatui) y emulación de pantalla con `vt100`. |

La UI y el acceso a red se comunican por canales (`std::sync::mpsc`): la app envía
`SshRequest` y el worker responde con `SshEvent`. Todo el estado `SSHClient` vive
dentro del worker, por lo que la TUI permanece fluida.

## Tests

```bash
cargo test
```

Cubren cifrado/descifrado, export/import de backups (ida y vuelta, contraseña
incorrecta y detección de manipulación), utilidades de rutas, encoding de teclas
para el PTY, formato de `known_hosts` y renderizado de la TUI (panel de perfiles y
pantalla del terminal) con `TestBackend`.

> Los tests usan un almacén temporal (`SecureStore::with_data_path`) y **nunca**
> leen ni escriben tus perfiles reales ni el archivo de configuración del usuario.


## Instalación y ejecución

```bash
cargo run
```

### Diagnóstico de caídas de sesión

Si una sesión se corta sin explicación, ejecuta la app con el log activado:

```bash
TUISSH_LOG=/tmp/tuissh.log cargo run
```

Cada evento relevante (conexión, desconexión, EOF del shell, error de lectura,
error de escritura, cierre del socket TCP) se anota con marca de tiempo en ese
archivo. Al cortarse la sesión, la app vuelve a la lista y muestra el motivo en
rojo durante unos segundos, aunque sigas escribiendo.

## Uso (atajos)

Todos los atajos de la app usan **un único prefijo: `Ctrl+Q`**. Así ninguna letra
suelta ni combinación `Ctrl+<letra>` dispara una acción por accidente.

En lista de perfiles:

- `Enter`: conectar por SSH
- `↑` / `↓`: mover la selección
- `Ctrl+Q` y luego:
  - `n`: nuevo perfil
  - `e`: editar perfil seleccionado
  - `d`: eliminar perfil seleccionado
  - `x`: exportar backup cifrado (pide ruta de destino y contraseña)
  - `r`: importar backup (pide archivo y contraseña)
  - `q`: salir
- `Ctrl+C`: salir (atajo de emergencia)

En sesión (terminal interactiva):

- Cualquier tecla se envía al shell remoto (incluye `Ctrl+C`, `Ctrl+D`, flechas,
  `Esc`, `Tab` para el autocompletado del shell remoto, etc.).
- `Ctrl+Q` (prefijo) abre los comandos de la app:
  - `Ctrl+Q` `Ctrl+D`: desconectar. **La segunda tecla lleva `Ctrl` a propósito**:
    así ninguna letra que escribas (por ejemplo la `d` de `cd /var/www/...`)
    puede cerrar la sesión, ni siquiera si el prefijo quedó armado por error.
  - `Ctrl+Q` `u`: subir archivo (`local_path remote_path`)
  - `Ctrl+Q` `r` o `Ctrl+Q` `o`: abrir/editar archivo remoto
  - `Ctrl+Q` `?`: ayuda
- Tras el prefijo, si la tecla **no** es un comando conocido se **reenvía al
  remoto** en vez de perderse, y el prefijo caduca solo a los 4 s.
- Si una sesión se cae, la app vuelve a la lista y muestra el motivo en rojo
  durante unos segundos (no se borra al seguir escribiendo).
- Se usa un prefijo para que `Ctrl+U`, `Ctrl+O` y `Esc` sigan funcionando dentro
  de programas como `vim` o `htop`.

En editor remoto:

- `Ctrl+S`: guardar cambios
- `Esc`: cancelar edición

## Seguridad

- Perfiles cifrados en el directorio de configuración del sistema
  (`~/Library/Application Support/tuissh/profiles.enc` en macOS,
  `~/.config/tuissh/profiles.enc` en Linux) con `AES-256-GCM`.
- Clave maestra guardada en keychain del sistema vía crate `keyring`.
- El archivo se guarda con permisos `0600` y el directorio con `0700`.
- **Backups cifrados con contraseña**: el archivo de backup es JSON con un sobre
  versionado (`format`, `version`, `kdf`, `salt`, `data`). Los perfiles —incluidas
  las contraseñas— se cifran con `AES-256-GCM` usando una clave derivada de tu
  contraseña con **Argon2id** (salt aleatorio de 16 bytes por backup). La contraseña
  del backup se pide al vuelo y **nunca se guarda**. Un backup con contraseña
  incorrecta o manipulado falla al descifrar (el tag GCM lo detecta). El archivo
  exportado también se escribe con permisos `0600`.
  - Al importar, la app **añade** los perfiles nuevos y **omite** los duplicados
    (comparando nombre + host + puerto + usuario); no borra nada existente.
- **Verificación de host key** contra `~/.ssh/known_hosts`:
  - Si coincide, conecta.
  - Si **cambió**, aborta la conexión con aviso de posible ataque MITM.
  - Si es un host nuevo, muestra su **huella SHA256** y pide escribir `yes` para
    confiar; solo entonces se añade a `~/.ssh/known_hosts` (sin reescribir el
    archivo). Antes de esa confirmación no se envía ninguna credencial.
- Si el keychain y el archivo cifrado quedan desincronizados (por ejemplo, borras la clave del keychain), la app respalda el archivo ilegible como `profiles.enc.unreadable.<timestamp>.bak`, arranca con perfiles vacíos y **muestra un aviso** con la ruta del respaldo.
- La contraseña de un backup debe tener **al menos 12 caracteres**; Argon2id usa 64 MiB y 3 pasadas (los backups antiguos siguen pudiéndose importar).

## Versión Go (macOS)

En [`goapp/`](goapp/) hay un port a Go como app de escritorio nativa (MyGo +
libghostty-vt), con barra lateral, terminal SSH embebida, Touch ID y el mismo
formato de `profiles.enc` y de backups que esta versión. Ver
[`goapp/README.md`](goapp/README.md) y
[`goapp/docs/DOCUMENTACION_TECNICA.md`](goapp/docs/DOCUMENTACION_TECNICA.md).
