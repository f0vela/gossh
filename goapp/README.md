# gossh-go

Port a Go de la app tuissh (Rust TUI), ahora gossh. App de escritorio nativa para macOS
hecha con [MyGo](https://mygo.egoist.dev): barra lateral con las conexiones y,
en una tarjeta redondeada, la terminal SSH embebida, emulada con
**libghostty-vt** (la librería de terminal de Ghostty, vía `plugins/terminal`).

Documentación técnica detallada: [`docs/DOCUMENTACION_TECNICA.md`](docs/DOCUMENTACION_TECNICA.md).

## Stack

- **UI**: `github.com/egoist/mygo` + `github.com/egoist/mygo/ui` — UI nativa
  en Go, dibujada en GPU (Metal en macOS), sin cgo.
- **Terminal**: `github.com/egoist/mygo/plugins/terminal` — emulación VT con
  libghostty-vt, scrollback, selección, copy/paste, zoom.
- **SSH/SFTP**: `golang.org/x/crypto/ssh` + `github.com/pkg/sftp`.
- **Keychain**: API nativa de macOS vía `mygo.Secrets` (servicio `gossh`,
  cuenta `data-key`, **el mismo que usa la app Rust** → comparte
  `profiles.enc`). La clave que dejó la app Rust se lee una vez con el CLI
  `security` y se migra (ver [Seguridad](#seguridad)).
- **Touch ID**: `LocalAuthentication` vía `purego` (puente Objective-C, sin cgo).

## Funcionalidades

- Crear, editar, duplicar, eliminar y listar perfiles SSH.
- Terminal interactiva real (PTY `xterm-256color`, resize con la vista,
  IXON/IXOFF desactivados, keepalive cada 20 s que cierra la sesión si la red
  murió). Vim, htop, tmux, sudo, etc. El teclado va a la terminal en cuanto
  conecta y al cerrar cualquier modal.
- Auth: password, llave privada (con/sin passphrase), keyboard-interactive, both.
  Si el servidor pide algo más (OTP, segundo factor, reintento tras contraseña
  errónea) la app abre un diálogo y te lo pregunta.
- Verificación de host key contra `~/.ssh/known_hosts`: conocida → conecta;
  distinta → rechaza (aviso MITM); desconocida → diálogo nativo con huella
  SHA256 y se añade solo si aceptas. Se ofrecen primero los algoritmos que ya
  tienes registrados, para no dar falsos «la llave cambió».
- SFTP: subir archivo (pide confirmación si va a sobrescribir), abrir/editar/
  guardar archivo remoto (límite 2 MiB, solo UTF-8, guardado atómico).
- Backup export/import cifrado con contraseña (Argon2id 64 MiB/3 pasadas +
  AES-256-GCM), **formato idéntico al Rust** (`gossh-backup` v1/v2): los
  backups exportados por la TUI se importan aquí directamente.
- Almacén cifrado `profiles.enc` en
  `~/Library/Application Support/gossh/` (0600).

## Interfaz

- **Ventana**: fondo propio (oscuro/claro según el sistema) con la barra
  lateral apoyada en él y el contenido en una **tarjeta** de esquinas
  redondeadas.
- **Barra lateral**: cabecera a la altura de los semáforos con iconos planos
  (mostrar/ocultar panel, importar, exportar, nueva conexión); debajo, filas
  compactas. La fila seleccionada es una pastilla con borde; una sesión viva
  muestra enchufe verde, tiempo conectado y ✓; ⌘N aparece al pasar el ratón.
- **Tarjeta**: cabecera con nombre y `usuario@host[:puerto]`, estado de la
  sesión y acciones (Subir, Editar remoto, Conectar/Desconectar); centro con
  la terminal (o un estado vacío / ficha de la conexión); pie con el estado y
  un chip del `usuario@host` activo.
- Avisos como toasts (los errores duran más). Errores de formularios dentro
  del propio modal.
- Terminal y tarjeta comparten el color de fondo para leerse como una
  sola superficie.

### Atajos y menús

| Atajo | Acción |
|---|---|
| ⌘, | Ajustes (menú **gossh → Ajustes…**) |
| ⌘L | Bloquear la app (si «Bloquear al abrir» está activo) |
| ⌘⌃S | Mostrar/ocultar la barra lateral |
| ⌘1 … ⌘9 | Conectar a la conexión n.º 1 … 9 (menú **Conexiones**) |
| Doble clic en una fila | Conectar |
| Clic derecho en una fila | Conectar · Editar · Duplicar · Eliminar |

## Seguridad

Ajustes en **gossh → Ajustes…** (⌘,). Usan LocalAuthentication: Touch ID y,
si falla o el Mac no tiene sensor, Apple Watch o la contraseña de macOS. Las
dos opciones vienen **desactivadas** y cambiarlas exige autenticarse:

- **Pedir Touch ID para conectar**: antes de usar, editar o exportar
  credenciales guardadas. La contraseña del servidor se escribe solo al crear
  la conexión.
- **Bloquear la app al abrirla**: no descifra ni muestra las conexiones hasta
  autenticarte; con ella el orden es huella → descifrar → listo. ⌘L la bloquea
  de nuevo.

Es una barrera de uso, no cifrado adicional: `settings.json` y `profiles.enc`
siguen siendo archivos del usuario, y una sesión SSH abierta sigue viva al
bloquear. Un archivo de ajustes corrupto bloquea la app (falla cerrado).

Resumen de las defensas (detalle en la documentación técnica):

- **Keychain**: solo se crea una clave nueva si el ítem no existe; cualquier
  otro fallo (keychain bloqueado, acceso denegado) se devuelve como error y
  nunca borra la clave del almacén existente. La clave que dejó el CLI
  `security`/la app Rust se lee una vez y se recrea con la app como dueña, de
  modo que el llavero ya no pide la contraseña en cada arranque.
- **Host keys**: `known_hosts` se crea (0600) si falta; cambio de llave =
  rechazo duro.
- **SFTP**: subida en streaming, lectura acotada, guardado atómico
  (temporal + renombrado, respeta symlinks y permisos), confirmación al
  sobrescribir.
- **Memoria**: passwords/passphrases y el texto del editor se borran al cerrar
  su modal, sea cual sea la forma de cerrarlo.
- **Título de ventana**: el que fija el servidor se limpia de caracteres de
  control, se acota y va siempre precedido por el nombre del perfil.
- **Backups**: contraseña mínima de 12 caracteres, costes KDF acotados al
  importar, archivo de hasta 16 MiB, escritura 0600.
- **libghostty-vt**: la descarga de desarrollo comprueba un SHA-256 fijado en
  el plugin; las apps empaquetadas no descargan nada.

### Límites conocidos

- Cifrar la clave con la propia huella (control de acceso biométrico del
  llavero / Secure Enclave) exige entitlements de un perfil de Apple; con la
  firma ad-hoc no es posible. Por eso Touch ID es una barrera de la app.
- La firma ad-hoc cambia en cada compilación: macOS puede pedir la contraseña
  del llavero una vez por versión nueva. Una identidad de firma estable
  (`macos.signingIdentity` en `mygo.json`) lo evita.
- Las contraseñas guardadas van en texto plano **dentro** de `profiles.enc`
  (formato compartido con la app Rust; el archivo sí va cifrado).
- Si Touch ID falla de forma inesperada pese a un dedo válido, hay que revisar
  la lectura del resultado en `internal/biometric`.

## Estructura

```
main.go                      arranque
internal/ui/app.go           estado, vistas, modales, menús, flujos
internal/sshx/               cliente SSH/SFTP, auth, known_hosts
internal/store/              perfiles cifrados, keychain, backups
internal/settings/           ajustes de seguridad (settings.json)
internal/biometric/          Touch ID / LocalAuthentication
build/dmg.sh                 genera .app + .dmg
```

## Desarrollo

```bash
go run .             # ejecutar (descarga libghostty-vt a la caché la 1ª vez)
go tool mygo dev     # modo dev con recarga
go vet ./... && go tool mygo vet .   # vet normal + ciclos de vida de la UI
go test ./...        # store, sshx, settings, biometric, ui
./build/dmg.sh       # → dist/darwin-arm64/{gossh.app, gossh *.dmg}
```

Los tests que tocan el llavero real están desactivados por defecto:

```bash
GOSSH_KEYCHAIN_TEST=1 go test ./internal/store -run Keychain
```

(usa un servicio temporal y no toca la clave de `gossh`).

`mygo build` firma ad-hoc y genera el `.app` + `.dmg` con libghostty-vt
incluida en el bundle (no se descarga nada en el Mac destino). Sin firma de
Apple: primera apertura con clic derecho → Abrir. La versión sale de
`mygo.json`.
