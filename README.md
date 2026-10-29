# Rate Limiter em Go

Desafio da Pós Go Expert (Full Cycle): um rate limiter usado como middleware HTTP que limita requisições por **IP** ou por **token de acesso** (header `API_KEY`), com persistência no **Redis**.

Ao exceder o limite, o servidor responde **HTTP 429** com o corpo exato:

```
you have reached the maximum number of requests or actions allowed within a certain time frame
```

e o IP ou token infrator fica bloqueado pelo tempo configurado (padrão de 5 minutos). Durante o bloqueio, todas as requisições dele são rejeitadas.

## Estrutura

```
cmd/server/                  # main: carrega config, escolhe a estratégia e sobe o servidor na 8080
internal/config/             # leitura de variáveis de ambiente e do .env
internal/limiter/            # domínio: regras de IP/token, bloqueio e a interface Storage (Strategy)
internal/middleware/         # middleware net/http: extrai IP e API_KEY, responde 429
internal/storage/redis/      # estratégia Redis (contagem atômica via script Lua)
internal/storage/memory/     # estratégia em memória (testes e instância única)
internal/storage/storagetest # suíte de contrato que toda estratégia deve passar
```

O pacote `limiter` não conhece HTTP nem Redis. O middleware só traduz a requisição em `Allow(ctx, ip, token)` e a decisão em resposta HTTP.

## Como funciona

- **Janela fixa.** Cada IP/token tem um contador `ratelimit:count:<ip|token>:<id>` que expira após `RATE_LIMIT_WINDOW` (padrão `1s`, ou seja, requisições por segundo). A janela começa na primeira requisição.
- **Bloqueio.** Quando o contador passa do limite, é gravada a chave `ratelimit:block:<ip|token>:<id>` com TTL `RATE_LIMIT_BLOCK_DURATION`. Enquanto ela existir, tudo é rejeitado com 429, mesmo que a janela já tenha sido reiniciada.
- **Atomicidade.** No Redis, `INCR` e `PEXPIRE` rodam num único script Lua, então o incremento e o TTL são atômicos mesmo com várias instâncias da aplicação. Na estratégia em memória, um mutex garante o mesmo comportamento.
- **Precedência Token > IP.** Se a requisição traz um `API_KEY` configurado, vale só o limite do token: o contador e o bloqueio do IP são ignorados. Exemplo: IP com 10 req/s e token `abc123` com 100 req/s, então a requisição com esse token aceita 100 req/s, mesmo vinda de um IP já bloqueado. Sem token, vale o limite do IP. O bloqueio de um token vale para qualquer IP que o use.
- **Token desconhecido.** Com `RATE_LIMIT_TOKEN_DEFAULT=0` (padrão), um token que não está em `RATE_LIMIT_TOKENS` é ignorado e a requisição é limitada pelo IP. Assim, quem inventa tokens aleatórios não escapa do limite por IP. Com um valor maior que zero, cada token desconhecido ganha esse limite próprio.
- **Falha do storage.** Se o Redis estiver indisponível, o middleware responde 500 e registra o erro no log (fail-closed).

## Configuração

Todas as opções vêm de variáveis de ambiente. Se existir um arquivo `.env` no diretório de execução, ele é carregado, mas uma variável já definida no ambiente prevalece sobre ele. O `docker-compose.yaml` usa o `.env` da raiz via `env_file`.

| Variável | Padrão | Descrição |
|---|---|---|
| `SERVER_PORT` | `8080` | Porta HTTP |
| `RATE_LIMIT_STORAGE` | `redis` | Estratégia de persistência: `redis` ou `memory` |
| `RATE_LIMIT_IP` | `10` | Máximo de requisições por janela para cada IP |
| `RATE_LIMIT_TOKENS` | vazio | Limites por token: `token:limite` separados por vírgula, ex.: `abc123:100,def456:50` |
| `RATE_LIMIT_TOKEN_DEFAULT` | `0` | Limite para tokens fora da lista; `0` faz o token desconhecido cair no limite por IP |
| `RATE_LIMIT_WINDOW` | `1s` | Tamanho da janela de contagem (duração Go: `1s`, `500ms`, `1m`) |
| `RATE_LIMIT_BLOCK_DURATION` | `5m` | Tempo de bloqueio após exceder o limite (`300s`, `5m`, `1h`) |
| `RATE_LIMIT_TRUST_FORWARDED_FOR` | `false` | Usa `X-Forwarded-For`/`X-Real-IP` como IP do cliente. Ative só atrás de um proxy confiável, pois o cliente pode forjar esses headers |
| `REDIS_ADDR` | `localhost:6379` | Endereço do Redis (no compose é sobrescrito para `redis:6379`) |
| `REDIS_PASSWORD` | vazio | Senha do Redis |
| `REDIS_DB` | `0` | Número do banco do Redis |

Exemplo de `.env`:

```env
RATE_LIMIT_IP=10
RATE_LIMIT_TOKENS=abc123:100,def456:50
RATE_LIMIT_TOKEN_DEFAULT=0
RATE_LIMIT_WINDOW=1s
RATE_LIMIT_BLOCK_DURATION=5m
REDIS_ADDR=localhost:6379
REDIS_PASSWORD=
REDIS_DB=0
```

O `.env` versionado contém apenas valores de exemplo, sem segredos. Em produção, defina `REDIS_PASSWORD` pelo ambiente.

## Trocando a estratégia de persistência

A persistência segue o padrão Strategy. O limiter depende só desta interface (`internal/limiter/limiter.go`):

```go
type Storage interface {
	Increment(ctx context.Context, key string, ttl time.Duration) (int64, error)
	Block(ctx context.Context, key string, duration time.Duration) error
	IsBlocked(ctx context.Context, key string) (bool, error)
}
```

- Para usar a estratégia em memória já existente, defina `RATE_LIMIT_STORAGE=memory`. Ela não compartilha estado entre instâncias.
- Para adicionar outro mecanismo (Memcached, PostgreSQL etc.):
  1. Crie um pacote em `internal/storage/<nome>` com um tipo que implemente `limiter.Storage`. `Increment` precisa ser atômico e definir o TTL só quando a chave é criada.
  2. Valide a implementação com a suíte de contrato: `storagetest.Run(t, newStorage, wait)`.
  3. Adicione um `case` em `newStorage` (`cmd/server/main.go`) e o nome aceito em `internal/config`.

Nenhuma alteração no limiter ou no middleware é necessária.

## Executando

Requisito: Docker com Docker Compose.

```bash
docker compose up -d --build
```

A aplicação sobe em `http://localhost:8080` junto com o Redis. Para testar:

```bash
# Sem token: a 11ª requisição dentro de 1s recebe 429
for i in $(seq 1 12); do curl -s -o /dev/null -w "%{http_code} " localhost:8080/; done; echo

# Com token de limite maior: passa mesmo com o IP bloqueado
for i in $(seq 1 12); do curl -s -o /dev/null -w "%{http_code} " -H "API_KEY: abc123" localhost:8080/; done; echo

# Ver os bloqueios e o tempo restante no Redis
docker compose exec redis sh -c 'for k in $(redis-cli --scan --pattern "ratelimit:block:*"); do echo "$k ttl=$(redis-cli ttl $k)s"; done'

docker compose down
```

Fora do Docker, com Go 1.26+ e um Redis acessível em `REDIS_ADDR`, rode `go run ./cmd/server`.

## Testes

Só com Docker (inclui os testes da estratégia Redis contra o Redis do compose, com o race detector):

```bash
docker compose run --rm --build test
docker compose down
```

Com Go instalado:

```bash
go test -race ./...                                   # os testes de Redis são pulados
REDIS_TEST_ADDR=localhost:6379 go test -race ./...    # inclui os testes de Redis
```

O que os testes cobrem:

- `internal/limiter`: limite por IP, limite por token, precedência Token > IP (inclusive com limite de token menor que o do IP e com o IP já bloqueado), token desconhecido, duração do bloqueio com relógio simulado (continua bloqueado após reiniciar a janela e libera após 5 min), reinício da janela e concorrência (100 goroutines com limite 10 resultam em exatamente 10 aceitas).
- `internal/middleware`: HTTP 429 com a mensagem exata, header `API_KEY` sobrepondo o IP (100 req com token e 10 sem), cliente bloqueado sem afetar outros IPs, 500 quando o storage falha, extração do IP.
- `internal/storage/memory` e `internal/storage/redis`: a mesma suíte de contrato (expiração do contador, expiração do bloqueio, atomicidade concorrente), TTL definido só na criação e um teste ponta a ponta do limiter sobre o Redis real.
- `internal/config`: leitura do `.env`, precedência do ambiente e validação de valores inválidos.
