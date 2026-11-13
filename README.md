# gossh — SSH manager

Gestor de conexiones SSH con terminal embebida, en Go. Dos apps, ambas llamadas gossh con las mismas funciones y el mismo
almacén cifrado (`~/Library/Application Support/gossh/`), así que puedes usar uno u otro con tus datos:

| Proyecto | Qué es | Stack |
|---|---|---|
| [`goapp/`](goapp) | gossh, app de escritorio nativa para macOS | Go + [MyGo](https://mygo.egoist.dev) |
| [`gossh-cli/`](gossh-cli) | gossh CLI, versión de terminal (binario `gossh`), con temas (Dracula, Tokyo Night, Catppuccin…) | Go + Bubble Tea + Lip Gloss |

Documentación técnica en [`docs/`](docs).
