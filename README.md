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
projgen new --out D:\Work\my-api     # хаана үүсгэхийг шууд заана
```

Төслийг хаана үүсгэхийг асууна. Enter дарвал `~/source/repos/<нэр>` (Windows дээр `C:\Users\<хэрэглэгч>\source\repos\<нэр>`) дотор үүснэ. `--config`-оор ажиллуулахад ч `--out` өгөөгүй бол мөн энэ хавтас руу үүснэ.

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

## Технологийн хувилбарууд

Үүсгэх төсөлд хамгийн сүүлийн хувилбаруудыг албан ёсны эх сурвалжаас авч ашиглана:

| Юу | Эх сурвалж | Аль хувилбар |
| --- | --- | --- |
| Go (go.mod, Dockerfile, CI) | go.dev/dl | Хамгийн сүүлийн stable |
| Gin, Echo, Fiber, pgx, MySQL driver | proxy.golang.org | `@latest` |
| PostgreSQL image | Docker Hub (official) | Хамгийн сүүлийн `<major>-alpine` (beta биш) |
| MySQL image | Docker Hub (official) | `lts` tag-ийн заадаг хувилбар |
| actions/checkout, actions/setup-go | GitHub releases | Сүүлийн major (`v7` г.м.) |

```bash
projgen update     # одоо шалгаж шинэчилнэ
projgen versions   # ашиглагдах хувилбаруудыг харуулна
projgen new --offline   # шалгахгүй, хадгалсан хувилбараар үүсгэнэ
```

`projgen new` хадгалсан хувилбар 24 цагаас хуучин бол автоматаар шалгана. Хувилбарууд `%APPDATA%\projgen\versions.json` (Linux: `~/.config/projgen/`) дотор хадгалагдана. Интернэтгүй үед хадгалсан эсвэл binary-д суулгасан хувилбарыг ашиглаж, анхааруулга хэвлэнэ.

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
