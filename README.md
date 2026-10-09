# projgen

Хөгжүүлэгчийн сонгосон технологиор (хэл, framework, архитектур, өгөгдлийн сан) шууд ажилладаг төслийн суурийг үүсгэдэг CLI. Хувилбар 1 нь Go-г дэмжинэ.

## Суулгах

```bash
go build -o bin/projgen .
```

## Ашиглах

```bash
projgen new                          # асуулт асууж сонгуулна
projgen new --config project.yaml    # файлаас уншина (CI-д тохиромжтой)
projgen new --dry-run                # юу үүсэхийг л харуулна
projgen list                         # дэмжигдэх бүх сонголт
```

`project.yaml` жишээ:

```yaml
name: order-service
module: github.com/MyagmardorjD/order-service
language: go
framework: gin            # gin | echo | fiber | nethttp
architecture: clean       # layered | clean | hexagonal
database: postgresql      # postgresql | mysql | none
extras: [docker, docker-compose, gitlab-ci, swagger]   # + github-actions
```

Үүссэн төсөл бүрт: `/health` (DB ping), `/api/v1/hello` жишээ endpoint, request_id-тай JSON logging, env-ээс уншдаг config, graceful shutdown, unit test.

## Шинэ framework нэмэх

1. `internal/options/options.go` дахь `Frameworks`-д нэмнэ.
2. `internal/generator/templates/http/<нэр>.go.tmpl` бичнэ (`Deps`, `NewRouter`, `Serve`).
3. Шаардлагатай бол `router_test.go.tmpl`-ийн `do` туслахыг өргөтгөнө.

## Тест

```bash
go test ./...                          # хурдан тестүүд
PROJGEN_E2E=1 go test ./internal/generator -run BuildAndTest -timeout 30m
```

Сүүлийнх нь 36 хослол бүрийг үүсгээд `go mod tidy`, `go vet`, `go test` ажиллуулна (интернэт хэрэгтэй).
