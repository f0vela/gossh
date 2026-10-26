# tuissh-go — Documentación técnica

Versión Go (macOS) de tuissh, hecha con MyGo. Complementa a
[`../README.md`](../README.md). El formato de datos (perfiles cifrados y
backups) es **idéntico al de la versión Rust**: ambas apps comparten
`profiles.enc` y los backups son intercambiables.

## 1. Visión general

```
┌───────────────────────── ventana (fondo propio) ─────────────────────────┐
│ ● ● ●   [panel][⤓][⤒][+]  │ ┌──────────── tarjeta redondeada ─────────┐ │
│ ──────────────────────────│ │ nombre  user@host   estado [Subir][Ed.] │ │
│ ✳ conexión A   2m ✓       │ │ [Conectar]                              │ │
│ ✳ conexión B      ⌘2      │ │                                         │ │
│                           │ │           terminal (libghostty)         │ │
│        barra lateral      │ │ ● estado                  [user@host]   │ │
└───────────────────────────┴─┴─────────────────────────────────────────┘ ┘
```

Un solo proceso, un solo hilo de UI (el hilo principal de macOS). El trabajo
bloqueante (SSH, SFTP, diálogos nativos, Touch ID, Argon2) corre en goroutines
y devuelve su resultado con `mygo.RunOnMain`.

## 2. Módulos

| Paquete | Responsabilidad |
|---|---|
| `main.go` | Llama a `ui.Run()`. |
| `internal/ui/app.go` | Estado de la app, vistas (barra lateral, tarjeta, modales), menús, conexión, SFTP, CRUD de perfiles, backups, bloqueo/ajustes. |
| `internal/sshx/client.go` | `Connect`, PTY + shell, resize, keepalive, SFTP (subida, lectura, escritura atómica, `Exists`). |
| `internal/sshx/auth.go` | Métodos de autenticación, keyboard-interactive (`kbChallenge`), llaves, callback de host key, alta en `known_hosts`. |
| `internal/sshx/knownalgos.go` | Algoritmos de host key ya registrados en `known_hosts` para un host. |
| `internal/sshx/posixpath.go` | Normalización de rutas POSIX remotas. |
| `internal/store/store.go` | Perfiles, AES-256-GCM, clave maestra en el llavero, backups Argon2id, fusión de importaciones. |
| `internal/settings/settings.go` | `settings.json` (opciones de seguridad). |
| `internal/biometric/` | `Available`, `Biometrics`, `Authenticate` sobre LocalAuthentication. |

## 3. Interfaz (`internal/ui`)

### 3.1 Estructura de la vista

`view()` drena los avisos pendientes a toasts, y según el estado:

1. **Bloqueada** → `lockView` (nada descifrado ni visible).
2. Si no, dibuja sobre `pal.win`: un `ui.Split` (ancho en `splitW`, arrastrable)
   con `sidebarPane` a la izquierda y la **tarjeta** (`card`) a la derecha. Con
   la barra oculta (`sidebarHidden`) la tarjeta ocupa toda la ventana.
3. `modals()` dibuja los modales encima.

Funciones principales: `sidebarPane` (cabecera + línea fina + `sidebarView`),
`sidebarControls` (iconos planos), `card` → `cardHeader` / `centerView` /
`statusBar`.

### 3.2 Paleta

`paletteOf(theme)` devuelve los colores propios (ventana, tarjeta, bordes,
selección, líneas, chip) para oscuro y claro. `termThemes()` construye los
temas del terminal con el **mismo fondo que la tarjeta** para que se fundan.
Los colores del texto vienen del tema del sistema (`c.Theme()`).

### 3.3 Panel central (`centerView`)

| Estado | Contenido |
|---|---|
| Sesión activa | Terminal con `AutoFocus()`; `Focus()` un frame cuando se cierra un modal. |
| Conectando | Spinner + mensaje. |
| Sin perfiles | Estado vacío con «Nueva conexión». |
| Sin selección | Estado vacío «Selecciona una conexión». |
| Perfil seleccionado | Ficha (puerto, método, llave) con Editar/Conectar. |

### 3.4 Filas de la barra lateral

Cada fila es un `ui.ButtonBase` (foco por teclado, `FocusGroup` vertical) con
icono, nombre, y a la derecha: si es la sesión viva, tiempo (`elapsed`) + ✓;
si no, `⌘N` al pasar el ratón o seleccionar. Clic selecciona, doble clic
conecta, clic derecho abre el menú. `tickElapsed` repinta cada 30 s mientras
hay sesión.

### 3.5 Menús

`menu()` construye la barra de menús: **tuissh** (Acerca de, Ajustes… ⌘,,
mostrar/ocultar barra ⌘⌃S, Bloquear ⌘L, Servicios, Ocultar, Salir),
Archivo, Edición, **Conexiones** (una entrada por perfil, ⌘1…⌘9), Ver,
Ventana. `refreshMenu()` lo reconstruye tras cada cambio de la lista y al
bloquear/desbloquear (bloqueada, no hay atajos de conexión).

### 3.6 Formulario de perfil

Agrupado en *Servidor* y *Autenticación*. Los campos de credenciales dependen
del método (`usesPassword`/`usesKey`); la llave se elige con un selector que
abre en `~/.ssh`. `validateForm` devuelve el perfil o un mensaje (puerto
1–65535, usuario obligatorio, credenciales según el método) que se muestra
**dentro** del modal; los campos que el método no usa se descartan al guardar.

### 3.7 Reglas de concurrencia (importante)

- `mygo.Dialog.Open/Save/Message` **bloquean** hasta que se cierra el diálogo y
  publican trabajo en el hilo principal: **nunca** se llaman desde un manejador
  de la UI (clic, vista), solo desde goroutines (deadlock: el sheet no se
  dibuja y la ventana se congela). `dialogBusy` evita apilar diálogos.
- `a.notify(kind, text)` encola avisos; solo la vista tiene el `ui.Context`
  que necesita un toast.
- Preguntas del servidor (`askPrompt`): la goroutine de conexión abre el modal
  con `RunOnMain` y espera en un canal con búfer; cerrar el modal sin
  responder cancela (`clearIdleSecrets` → `answerPrompt("", false)`).

## 4. Conexión SSH (`internal/sshx`)

`Connect(profile, cols, rows, confirm, prompt)`:

1. `authMethods` según el método del perfil (ver 4.1).
2. `hostKeyCallback` sobre `~/.ssh/known_hosts` (ver 4.2); se fijan
   `HostKeyAlgorithms` con `knownHostKeyAlgorithms`.
3. `ssh.Dial` (timeout 20 s, `net.JoinHostPort` → IPv6 correcto), cliente SFTP,
   sesión con PTY `xterm-256color` y `IXON/IXANY/IXOFF = 0`, shell.
4. `keepalive` cada 20 s; un error de transporte cierra la conexión.

### 4.1 Autenticación

| Método | Orden |
|---|---|
| `password` | password, keyboard-interactive |
| `key` | llave |
| `both` | llave, password, keyboard-interactive |
| `interactive` | keyboard-interactive, password |

`kbChallenge`: la contraseña guardada responde **solo a la primera** pregunta
oculta que parezca de contraseña; cualquier otra (OTP, 2FA, reintento tras un
fallo) va al usuario mediante `Prompt`. Sin `Prompt`, o si se cancela, la
autenticación falla con un error claro.

### 4.2 Host keys

- `known_hosts` se crea (dir 0700, archivo 0600) si no existe: antes, un
  archivo ausente dejaba un callback nulo y la app se caía.
- Coincide → conecta. Distinta (`KeyError.Want` no vacío) → rechazo duro con
  la huella recibida. Desconocida → `confirm` (diálogo nativo con tipo y
  huella SHA256); si aceptas se añade con `knownhosts.Line`.
- `knownHostKeyAlgorithms` lee el archivo (entradas en claro y hasheadas
  `|1|salt|hmac`) y ofrece primero el tipo de llave ya registrado, como
  OpenSSH; así un servidor que prefiere otro tipo no parece «cambiado».

### 4.3 SFTP

| Operación | Detalle |
|---|---|
| `UploadFile` | Streaming con `io.Copy`; crea el directorio padre. |
| `Exists` | `Stat` de la ruta resuelta; la UI pide confirmar antes de sobrescribir. |
| `ReadRemoteFile` | Tope `MaxEditableSize` (2 MiB); rechaza contenido no UTF-8 (editarlo lo corrompería). |
| `WriteRemoteFile` | Escribe un temporal `*.tuissh-tmp`, `Chmod` al modo original y `PosixRename` sobre el destino (fallback: borrar + renombrar, conservando el temporal si falla). Sigue symlinks (`RealPath`). Una caída a mitad nunca deja el archivo truncado. |

## 5. Almacén y llavero (`internal/store`)

- `profiles.enc` (0600) en `~/Library/Application Support/tuissh/`:
  `nonce(12) ‖ AES-256-GCM(JSON de perfiles)`. Escritura atómica
  (temporal + `rename`).
- Si no se puede descifrar, el archivo se **mueve** a
  `profiles.enc.unreadable.<ts>.bak` (nunca se borra) y se avisa.
- **Clave maestra**: 32 bytes, base64, en el llavero (servicio `tuissh`, cuenta
  `data-key`) mediante `mygo.Secrets` (API nativa). Reglas:
  - Solo se genera una clave nueva si el ítem **no existe**
    (`ErrSecretNotFound` o exit 44 del CLI). Cualquier otro error se devuelve.
  - Un ítem hecho por la app Rust o por el CLI `security` no se ve con la API
    nativa: se lee una vez con el CLI (que ese ítem sí acepta), se borra y se
    **recrea con la app como propietaria** (`migrateLegacyKey`). Si la
    escritura nativa fallara tras borrar, la clave se restaura con el CLI para
    no perderla. Todas las llamadas al CLI tienen un plazo de 60 s.
  - Una clave inválida con datos existentes es un error, no se regenera.
- `AppendNew` / `MergeImported`: fusión sin duplicados
  (`nombre|host|puerto|usuario`).

### 5.1 Backups

Envelope JSON `tuissh-backup` v2 (v1 legado): `kdf=argon2id`, `salt`,
`m_cost/t_cost/p_cost`, `data` (AES-256-GCM). Nuevos: 64 MiB, 3 pasadas.
Contraseña ≥ 12 caracteres; al importar los costes se acotan
(`maxKDF*`) y la UI rechaza archivos > 16 MiB (`MaxBackupFileSize`). La
exportación (Argon2 + diálogo de guardado) corre fuera del hilo de la UI y se
escribe con 0600.

## 6. Ajustes y Touch ID

### 6.1 `internal/settings`

```json
{ "require_on_launch": false, "require_for_credentials": false }
```

`settings.json` (0600, atómico). **Un archivo corrupto es un error, no un
reset**: la app se bloquea en vez de perder las protecciones.

### 6.2 `internal/biometric`

Carga `LocalAuthentication` con `purego` y habla con `LAContext` a través del
puente Objective-C (`objc.NewBlock` para el `reply:`). Política
`LAPolicyDeviceOwnerAuthentication` (Touch ID → Apple Watch → contraseña).

- `Biometrics()` / `Available()`: `canEvaluatePolicy` con las políticas 1 y 2.
- `Authenticate(reason)`: bloquea hasta el resultado; **no** llamar desde la UI.
  El `BOOL` de retorno se lee solo en su byte bajo. Cancelaciones
  (`-2`, `-4`, `-9`) → `ErrCancelled` (silencioso en la UI).
- En otros SO, `biometric_other.go` devuelve «no disponible».

### 6.3 Flujo en la app

- `initSecurity`: calcula `authKind` («Touch ID», «contraseña de macOS» o
  vacío), lee ajustes; `locked = RequireOnLaunch && authKind != ""`.
- Arranque bloqueado: la ventana se crea, `unlock()` autentica y **solo
  entonces** `loadProfiles()` toca el llavero y descifra.
- `guard(reason, fn)`: si `RequireForCredentials`, autentica antes de
  `connectTo`, `openForm` (editar) y `exportBackup`.
- `changeSetting`: cualquier cambio de ajustes exige autenticarse.
- `lock()` (⌘L): oculta la UI y quita los atajos de conexión; no cierra la
  sesión viva ni vacía los perfiles de memoria.
- `authBusy` impide abrir dos avisos del sistema a la vez.

## 7. Higiene de secretos y entrada

- `clearIdleSecrets` (cada frame): si un modal no está abierto, borra
  `fPass/fKeyPwd` (formulario), `bkPass/bkPass2/bkData` (backup),
  `editorText` y la respuesta pendiente de una pregunta.
- `windowTitle(perfil, remoto)`: el título que fija el servidor se limpia
  (`unicode.IsControl`), se acota a 80 runas y va tras el nombre del perfil.
- Los errores de formulario/backup se muestran en el modal, no en un estado
  tapado por él.

## 8. Pruebas

| Paquete | Qué cubre |
|---|---|
| `store` | Roundtrip de cifrado; backups (roundtrip, contraseña incorrecta o corta, sobre v1 legado, costes KDF fuera de rango); escritura 0600; fusión sin duplicados. |
| `store` (`TUISSH_KEYCHAIN_TEST=1`) | Migración de un ítem legado del llavero con un servicio temporal. |
| `sshx` | `known_hosts` ausente → pregunta y persiste (0600); llave cambiada → rechazo sin preguntar; llave rechazada → no se escribe; algoritmos conocidos (claro/hasheado/puerto); enrutado de preguntas keyboard-interactive. |
| `settings` | Roundtrip, permisos 0600, archivo corrupto = error. |
| `biometric` | La respuesta del bloque llega desde el hilo del sistema y la cancelación se mapea (sin intervención humana); disponibilidad sin pánico. |
| `ui` | Título remoto sanitizado, validación del formulario, `guard`, formato de `elapsed`. |

No hay pruebas automáticas del éxito real de Touch ID, del aspecto de la UI ni
de una sesión SSH completa: se verifican a mano.

```bash
go vet ./... && go tool mygo vet .
go test ./...
```

## 9. Compilación y distribución

`./build/dmg.sh` → `go tool mygo build`: compila, firma **ad-hoc** el `.app`
(con `libghostty-vt.dylib` en `Contents/Resources`) y crea
`tuissh <versión> arm64.dmg`. La versión y `minimumSystemVersion` (13.0) están
en `mygo.json`. Sin Developer ID: primera apertura con clic derecho → Abrir.

La firma ad-hoc tiene un designated requirement basado en `cdhash`, que cambia
en cada compilación; el llavero, por tanto, vuelve a pedir la contraseña una
vez por versión nueva. Poner una identidad estable en
`macos.signingIdentity` lo evita.

## 10. Límites conocidos

- Touch ID es una barrera de la app, no cifrado: ligar la clave a la huella
  exige entitlements de un perfil de Apple.
- Un proceso con el usuario puede editar `settings.json`.
- Las contraseñas guardadas van en claro dentro del JSON cifrado de
  `profiles.enc` (compatibilidad con la app Rust).
- Las subidas pisan archivos solo tras confirmar; la edición remota exige
  texto UTF-8 de hasta 2 MiB.
- El fondo de la barra lateral usa el color de la paleta, no el material
  translúcido nativo de macOS; la navegación con flechas entre filas depende
  del grupo de foco vertical y no se ha probado a fondo.
