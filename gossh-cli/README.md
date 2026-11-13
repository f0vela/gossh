# gossh

Gestor SSH en la terminal, hecho con **Go + [Bubble Tea](https://github.com/charmbracelet/bubbletea) + [Lip Gloss](https://github.com/charmbracelet/lipgloss)** (v2).
Es la versión de terminal de [`goapp`](../goapp) (gossh, app de escritorio): mismas funciones y **el mismo
almacén de datos**, así que tus conexiones, snippets y backups funcionan en las dos.

```
go build -o dist/gossh .      # o: make build
./dist/gossh
```

## Funciones (paridad con goapp)

- Crear, editar, duplicar, eliminar y buscar conexiones SSH.
- Terminal interactiva real (PTY `xterm-256color`, emulada con `charmbracelet/x/vt`): vim, htop, tmux, sudo…
  Redimensionado, historial (scrollback de 5000 líneas), pegado, IXON/IXOFF desactivados, keepalive de 20 s.
- Auth: contraseña, llave privada (con/sin passphrase), keyboard-interactive, o llave+contraseña. Si el servidor pide
  algo más (OTP, segundo factor) se abre un diálogo.
- Verificación de host key contra `~/.ssh/known_hosts` (desconocida → confirmación con huella SHA256; cambiada → rechazo).
- SFTP: subir archivo (confirma antes de sobrescribir) y editar archivos remotos (≤ 2 MiB, UTF-8, guardado atómico).
- Snippets cifrados: pegar en la sesión (con o sin Enter), crear, editar, borrar, buscar.
- Backup export/import cifrado con contraseña (Argon2id + AES-256-GCM), formato `gossh-backup` v1–v3, incluye snippets.
- Seguridad: «Pedir Touch ID para conectar» y «Bloquear al abrir» (Touch ID / contraseña de macOS).
- Almacén `profiles.enc` / `snippets.enc` en `~/Library/Application Support/gossh/`, clave en el Llavero
  (servicio `gossh`, cuenta `data-key`).

## Temas

16 temas: Tokyo Night (por defecto) y Storm, Dracula, Catppuccin (Mocha, Macchiato, Frappé, Latte), Nord, Gruvbox,
Solarized (dark/light), One Dark, Rosé Pine, Kanagawa, Monokai y GitHub Light. El tema pinta la interfaz **y** los
16 colores ANSI de la terminal remota.

| Cómo | Qué hace |
|---|---|
| `t` (o `Ctrl+Q t` en la terminal) | selector con **vista previa en vivo**; `enter` aplica y guarda, `esc` cancela |
| `T` | siguiente tema |
| `,` → «Tema» | cambia con ←/→ desde Ajustes |
| `gossh --theme dracula` | tema solo para esa ejecución |
| `gossh theme list` / `set NOMBRE` / `current` | ver, guardar por defecto, consultar |

El tema guardado vive en `settings.json` (campo `theme`).

## Teclas

Barra lateral enfocada:

| Tecla | Acción | Tecla | Acción |
|---|---|---|---|
| ↑↓ / j k | mover | `n` | nueva conexión |
| `enter` / `1`–`9` | conectar | `e` / `c` / `d` | editar / duplicar / eliminar |
| `tab` | ir a la terminal | `x` | desconectar |
| `/` | buscar | `u` / `o` | subir archivo / editar archivo remoto |
| `s` | snippets | `i` / `E` | importar / exportar backup |
| `t` / `T` | tema / siguiente | `,` / `l` | ajustes / bloquear |
| `b` | mostrar/ocultar barra | `?` | ayuda y paleta de comandos |
| `q` | salir | | |

Con la terminal enfocada **todo va al servidor**; `Ctrl+Q` es la tecla líder y la siguiente tecla elige la acción:
`q` volver a la barra · `d` desconectar · `u` subir · `e` editar remoto · `s` snippets · `t` tema · `x` exportar ·
`r` importar · `l` bloquear · `,` ajustes · `b` barra · `?` ayuda · `PgUp/PgDn` historial · `Ctrl+Q` envía Ctrl+Q.
`Shift+PgUp/PgDn` también desplaza el historial. En los formularios, `Tab` completa rutas de archivo.

## Estructura

```
main.go                  flags y subcomando `theme`
internal/tui/            modelo Bubble Tea, vistas, modales, sesión + emulador VT
internal/theme/          paletas
internal/sshx/           cliente SSH/SFTP, auth, known_hosts
internal/store/          perfiles, snippets, backups, llavero (CLI `security`)
internal/settings/       ajustes (seguridad + tema)
internal/biometric/      Touch ID (LocalAuthentication vía purego)
internal/paths/          directorio de datos
```

## Desarrollo

```
go vet ./... && go test ./...
```

Variables solo para pruebas, para no tocar tus datos reales: `GOSSH_DATA_DIR` (directorio de datos),
`GOSSH_KEYRING_SERVICE` (servicio del Llavero) y `GOSSH_KNOWN_HOSTS` (archivo known_hosts).
`GOSSH_KEYCHAIN_TEST=1 go test ./internal/store -run Keychain` prueba el Llavero real con un servicio temporal.

## Notas

- La primera vez, macOS puede preguntar si `security` puede leer la clave que creó la app de escritorio:
  elige «Permitir siempre».
- Fuera de macOS la clave maestra se guarda en un archivo `0600` junto al almacén (sin Llavero ni Touch ID).
- No hay ratón: el historial se recorre con el teclado y la selección de texto es la de tu terminal.
