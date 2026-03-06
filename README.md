# tracker-time

Daemon de monitoramento de produtividade para Linux (**X11**). Roda em segundo plano, sem interface gráfica, registrando a janela ativa e o tempo de uso, e sincronizando com uma API REST.

## Requisitos

- **Go** 1.21+
- **Linux** com sessão gráfica X11
- `DISPLAY` configurado; **xprintidle** (opcional, para tempo ocioso): `sudo apt install xprintidle`

## Estrutura do projeto

- `main.go` — Ponto de entrada, config, DB, goroutines de monitor e sync
- `providers.go` — Interfaces `IdleProvider` e `WindowProvider`
- `provider_x11.go` — Idle (xprintidle) e janela ativa (xgb) para X11
- `schema.sql` — DDL de referência da tabela SQLite
- `tracker-time.service` — Exemplo de unidade systemd

## Compilação (Linux)

```bash
cd /caminho/para/tracker-time
go mod tidy
CGO_ENABLED=1 go build -o tracker-time .
```

O `mattn/go-sqlite3` exige CGO; para compilar para outro host Linux (cross-compile) use, por exemplo:

```bash
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -o tracker-time .
```

## Banco de dados (SQLite)

O programa usa SQLite. Por padrão o arquivo fica em:

- `~/.local/share/tracker-time/tracker.db`

Para mudar o caminho, defina a variável de ambiente:

```bash
export TRACKER_DB_PATH=/caminho/para/tracker.db
```

### Variáveis de ambiente (resumo)

| Variável                                  | Descrição                                                                                     | Padrão                                   |
| ----------------------------------------- | --------------------------------------------------------------------------------------------- | ---------------------------------------- |
| `TRACKER_DB_PATH`                         | Caminho do arquivo SQLite                                                                     | `~/.local/share/tracker-time/tracker.db` |
| `TRACKER_INGEST_URL` ou `TRACKER_API_URL` | URL base da API de ingestão (incluir path completo, ex.: `https://api.example.com/v1/ingest`) | `https://api.dashboard.com/v1/ingest`    |
| `TRACKER_IDLE_THRESHOLD`                  | Tempo sem mouse/teclado para considerar inativo (ex.: `2s`, `60s`, `1m`)                      | `2s`                                     |
| `TRACKER_TTL` / `TRACKER_TTL_HOURS`       | TTL dos registros locais (ver seção abaixo)                                                   | 7 dias                                   |

### TTL (expiração de registros antigos)

O **SQLite não tem TTL nativo** (como DynamoDB ou Redis). O daemon implementa TTL na aplicação: a cada ciclo de sync, apaga registros cujo `start_time` seja anterior ao limite configurado. Assim, se a API ficar fora do ar por muito tempo, a tabela não cresce indefinidamente.

- **Padrão:** 7 dias (`168h`).
- **Variáveis de ambiente:**
  - `TRACKER_TTL` — duração em formato Go (ex.: `72h`, `24h`, `0` para desativar).
  - `TRACKER_TTL_HOURS` — número de horas (ex.: `168` = 7 dias).
- Exemplo: `export TRACKER_TTL=24h` para descartar registros com mais de 24 horas.

### DDL (CREATE TABLE)

Ver `schema.sql`. A tabela principal é:

```sql
CREATE TABLE IF NOT EXISTS activity (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	process_name TEXT NOT NULL,
	window_title TEXT NOT NULL,
	start_time DATETIME NOT NULL,
	end_time DATETIME NOT NULL,
	user_name TEXT NOT NULL DEFAULT '',
	hostname TEXT NOT NULL DEFAULT '',
	local_ip TEXT NOT NULL DEFAULT '',
	network_ip TEXT NOT NULL DEFAULT ''
);
```

## Execução como daemon

### Opção 1: nohup (rápido)

```bash
./tracker-time &
# ou, para desconectar do terminal e manter rodando após logout:
nohup ./tracker-time > /tmp/tracker-time.log 2>&1 &
```

### Opção 2: systemd (recomendado)

Como **serviço de usuário** (recomendado para ter acesso ao DISPLAY da sessão gráfica):

1. Instale o binário, por exemplo:

   ```bash
   cp tracker-time ~/.local/bin/
   # ou: sudo cp tracker-time /usr/local/bin/
   ```

2. Copie a unit para o systemd do usuário:

   ```bash
   mkdir -p ~/.config/systemd/user
   cp tracker-time.service ~/.config/systemd/user/
   ```

   O `ExecStart` já usa `%h` (home do usuário), então não é preciso editar caminhos — desde que o binário esteja em `~/.local/bin/tracker-time`. Se quiser DB ou API em outro lugar, descomente e edite as linhas `Environment=` na unit.

3. Recarregue e ative:

   ```bash
   systemctl --user daemon-reload
   systemctl --user enable --now tracker-time
   ```

4. Verificar status:
   ```bash
   systemctl --user status tracker-time
   ```

Para rodar como **serviço do sistema** (menos comum, pois precisa de DISPLAY do usuário), copie a unit para `/etc/systemd/system/`, edite `ExecStart` e possivelmente `User=`/`Environment=DISPLAY=:0`, depois:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now tracker-time
```

## Comportamento

- **Goroutine de monitoramento:** a cada 2 s lê a janela ativa e o título via X11, verifica tempo ocioso (xprintidle). Atualiza ou insere um registro no SQLite; se o usuário estiver ocioso acima do threshold configurado, não atualiza.
- **Goroutine de sincronização:** a cada 10 min busca registros, monta um JSON e envia `POST` para a API de ingestão. Em resposta 200 OK, remove os registros locais.

Encerramento: SIGINT ou SIGTERM (por exemplo ao parar o serviço systemd) finaliza o processo de forma limpa.
