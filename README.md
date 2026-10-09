# projgen

[![ci](https://github.com/MyagmardorjD/projgen/actions/workflows/ci.yml/badge.svg)](https://github.com/MyagmardorjD/projgen/actions/workflows/ci.yml)
[![e2e](https://github.com/MyagmardorjD/projgen/actions/workflows/e2e.yml/badge.svg)](https://github.com/MyagmardorjD/projgen/actions/workflows/e2e.yml)

Хөгжүүлэгчийн сонгосон технологиор (хэл, framework, архитектур, өгөгдлийн сан) шууд ажилладаг төслийн суурийг үүсгэдэг хэрэгсэл. Вэб интерфейс болон CLI-тэй.

| Хэл | Framework | Build |
| --- | --- | --- |
| Go | Gin, Echo, Fiber, net/http | `go` |
| Java | Spring Boot (Log4j 2 JSON logging, Logback хасагдсан) | Maven Wrapper (`mvnw`), JDK = хамгийн сүүлийн LTS |

Хоёулаа Layered / Clean / Hexagonal бүтэц, PostgreSQL / MySQL / DB-гүй сонголттой.

## Суулгах

```bash
go install github.com/MyagmardorjD/projgen@latest
```

## Вэб интерфейс

```bash
projgen serve
```

Браузерт `http://127.0.0.1:8090` нээгдэнэ. Тэнд:

1. Нэр, framework, архитектур, өгөгдлийн сан, нэмэлтүүдээ сонгоно. Үүсэх файлууд баруун талд шууд харагдана.
2. **Компьютер дээр үүсгэх** дарвал сонгосон хавтсанд (анхдагч `~/source/repos`) үүсгээд `go mod tidy`, `git init` хийнэ. Дараа нь VS Code эсвэл Explorer-оор нээж болно.
3. Эсвэл **ZIP татах** дарж архиваар авна.
4. **Шинэчлэх** нь технологийн хувилбаруудыг албан ёсны эх сурвалжаас дахин шалгана.

Сервер зөвхөн `127.0.0.1` дээр сонсоно. Өөр вэбсайт таны дискэнд файл бичүүлэхээс хамгаалж, хүсэлт бүр хуудсанд суулгасан токен болон Host-ыг шалгадаг. `--port`, `--no-browser`, `--offline` flag-тай.

## CLI

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
extras: [docker, docker-compose, gitlab-ci, swagger, migrations]   # + github-actions, auth, observability, redis
```

`migrations` (DB сонгосон үед): асахдаа migration-ийг автоматаар ажиллуулна. Go: `migrations/*.sql`-ийг binary-д суулгаж golang-migrate-ээр, Java: `db/migration/V*.sql`-ийг Flyway-ээр. `MIGRATE_ON_START=false` гэж унтраана. `projgen add entity`-ийн migration ч дараагийн асалтад автоматаар ажиллана.

| Нэмэлт | Go | Java (Spring Boot) |
| --- | --- | --- |
| `auth` | `/api/v1/*` бүхэлдээ `Authorization: Bearer <JWT>` шаардана (HS256, `sub`+`exp` заавал, `JWT_SECRET` ≥ 32 байт, `JWT_ISSUER`/`JWT_AUDIENCE` сонголттой). golang-jwt, `GET /api/v1/me`, хөгжүүлэлтийн token: `go run ./cmd/token -sub alice` | Spring Security resource server (`SecurityConfig`), `MeController`, бодит сервер дээрх `ServerTests` |
| `observability` | `GET /metrics` (Prometheus: `http_request_duration_seconds{method,route,status}` + Go runtime), хүсэлт бүрт OpenTelemetry span (`traceparent` үргэлжилнэ), логт `trace_id`/`span_id`. `OTEL_EXPORTER_OTLP_ENDPOINT` өгвөл OTLP/HTTP-ээр илгээнэ | Actuator + Micrometer: `GET /metrics`, OpenTelemetry tracing (`TRACING_EXPORT_ENABLED=true` үед илгээнэ), логт `trace_id`/`span_id` |
| `redis` | `cache` пакет (go-redis: JSON `Get`/`Set`/`Delete`, TTL, үйлчилгээний нэрийн угтвар), `/health` Redis-ийг ping хийнэ, `TEST_REDIS_URL`-тэй тест | Spring Data Redis + `@EnableCaching` (`@Cacheable`, `CACHE_TTL`), `/health` Redis-ийг ping хийнэ |

`/health` болон `/metrics` нийтэд нээлттэй хэвээр. Нууц утгуудад анхдагч утга байхгүй (docker-compose ч `JWT_SECRET`-ийг `.env`-ээс шаардана). docker-compose сонгосон бол `redis` service нэмэгдэнэ.

Java төслийн хувьд:

```yaml
name: order-service
module: com.techpartners.orderservice   # Java base package
language: java
framework: spring-boot
architecture: clean
database: postgresql
```

Үүссэн төсөл бүрт: `/health` (DB ping), `/api/v1/hello` жишээ endpoint, request_id-тай JSON logging, env-ээс уншдаг config, graceful shutdown, unit test.

Java төсөлд `RequestIdFilter` хүсэлт бүрт `ThreadContext.put("request_id", ...)` хийж, дараа нь `ThreadContext.clearAll()` хийнэ. Log4j 2-ийн `JsonTemplateLayout` нь `@timestamp`, `level`, `message`, `service`, `env`, `request_id`, `logger` талбартай JSON бичнэ. `pom.xml`-ээс Logback-ийг хасдаг. Swagger сонговол springdoc нэмэгдэнэ (`/swagger-ui.html`).

## Preset (багийн стандарт)

Preset нь хэл, framework, бүтэц, DB, нэмэлтүүд болон module-ийн угтварыг нэрээр нь хадгалсан YAML файл юм. Баг бүх сервисээ нэг ижил стекээр эхлүүлэхэд хэрэглэнэ.

```bash
projgen preset list                                   # бүх preset
projgen new --preset techpartners-go                  # зөвхөн нэр, module, байршлыг асууна
projgen new --preset techpartners-java --name pay-api # юу ч асуухгүй
projgen preset save backend-go --from project.yaml --description "Backend багийн Go стандарт"
```

`projgen new`-ийн эхний асуулт нь preset сонголт байна (Enter = бүгдийг өөрөө сонгох). Вэб хуудасны дээд хэсэгт preset сонгох жагсаалт, доод хэсэгт "preset болгон хадгалах" хэсэг бий.

Preset гурван газраас ачаалагдана. Ижил нэртэй бол доорхи нь дээрхийгээ дарна:

| Эх үүсвэр | Хаана | Жишээ |
| --- | --- | --- |
| Суулгасан | projgen-д | `techpartners-go`, `techpartners-java` (хоёулаа migration-тэй), `go-minimal` |
| Баг | `PROJGEN_PRESETS` хувьсагчид заасан хавтсууд (`;`-ээр, Linux/macOS дээр `:`-ээр тусгаарлана) | clone хийсэн багийн repo, хуваалцсан диск |
| Хэрэглэгч | `%APPDATA%\projgen\presets` (Linux: `~/.config/projgen/presets`) | `projgen preset save`-ээр хадгалсан |

Preset файлын жишээ (`backend-go.yaml`):

```yaml
name: backend-go
description: Backend багийн Go стандарт
language: go
framework: echo
architecture: hexagonal
database: postgresql
extras: [docker, docker-compose, gitlab-ci]
module_prefix: gitlab.techpartners.asia/backend   # module = <prefix>/<нэр>
```

Багтай хуваалцахын тулд preset файлуудаа нэг git repo-д хийж, гишүүн бүр clone хийгээд `PROJGEN_PRESETS`-д тэр хавтсыг заана. Буруу preset файл байвал алгасаж, анхааруулга хэвлэнэ.

## Entity нэмэх (CRUD)

Үүссэн төслийн хавтсанд:

```bash
projgen add entity Product name:string:required description:text price:float stock:int active:bool released_at:time
```

Go болон Java төсөлд ажиллана. Талбар нь `нэр:төрөл` эсвэл `нэр:төрөл:required` (required нь string, text-д). Төрлүүд: `string` (255 тэмдэгт), `text`, `int`, `int64`, `float`, `bool`, `time`. `id`, `created_at`, `updated_at` автоматаар нэмэгдэнэ. Нэг секундэд хэд хэдэн entity нэмсэн ч migration-ий хувилбар давхцахгүй.

`project.yaml`-аас хэл, бүтэц, framework, DB-г уншаад тухайн бүтэцт тохируулж үүсгэнэ. Go төсөлд:

| Файл | Агуулга |
| --- | --- |
| domain `product.go` | `Product`, `ProductInput` + шалгалт, `ProductRepository` interface |
| service `product_service.go` + тест | Бизнес дүрэм, хуудаслалт (анхдагч 20, дээд тал 100) |
| repository `product_repository.go` | PostgreSQL/MySQL SQL, DB-гүй бол санах ойд |
| repository `product_repository_test.go` | `TEST_DATABASE_URL` өгвөл жинхэнэ DB дээр migration хийж CRUD шалгана |
| http `product_handler.go` + тест | `POST/GET /api/v1/products`, `GET/PUT/DELETE /api/v1/products/{id}` |
| `migrations/<огноо>_create_products.up.sql` / `.down.sql` | Хүснэгт үүсгэх / устгах |

`router.go`, `main.go` дахь `// projgen:` тэмдэгтэй мөрийн өмнө шинэ entity-г автоматаар холбоно. Тэмдэггүй хуучин төсөлд юуг гараар нэмэхийг хэвлэнэ. Байгаа entity-г дахин үүсгэхэд `--force` хэрэгтэй.

Java (Spring Boot) төсөлд:

| Файл | Агуулга |
| --- | --- |
| domain `Product.java`, `ProductInput.java`, `ProductRepository.java` | record-ууд (JSON нь `snake_case`), шалгалт, repository interface |
| domain `NotFoundException.java`, `ValidationException.java` | Бүх entity-д нэг удаа үүснэ |
| service `ProductService.java` + тест | Бизнес дүрэм, хуудаслалт (анхдагч 20, дээд тал 100) |
| repository `JdbcProductRepository.java` | `JdbcClient`-ээр PostgreSQL/MySQL, DB-гүй бол `InMemoryProductRepository` |
| repository `JdbcProductRepositoryTest.java` | `TEST_DATABASE_URL` (JDBC) өгвөл жинхэнэ DB дээр CRUD шалгана |
| web `ProductController.java` + MockMvc тест | Go-тай ижил endpoint-ууд |
| web `EntityExceptionHandler.java` | `404` / `422` / `400` хариуг `ApiExceptionHandler`-ээс өмнө буцаана |
| `db/migration/V<огноо>__create_products.sql` | Flyway migration (`migrations` сонгосон бол асахдаа автоматаар) |

Spring component scan хийдэг тул юу ч гараар холбох шаардлагагүй. Java-гийн түлхүүр үг (`class`, `new` г.м.) болон `String`, `List` зэрэг класстай давхцах нэрийг хүлээж авахгүй. Хоосон ирсэн тоо, bool талбар 0/false болно, `time` талбарыг заавал илгээнэ.

Алдааны хариу (хоёр хэлэнд ижил): шалгалт буруу бол `422`, олдоогүй бол `404`, буруу id/JSON бол `400`.

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

Сүүлийнх нь 36 хослол бүрийг үүсгээд `go mod tidy`, `go vet`, `go test` ажиллуулна (интернэт хэрэгтэй). `PROJGEN_E2E_LATEST=1` нэмбэл binary-д суулгасан биш, албан ёсны эх сурвалжийн хамгийн сүүлийн хувилбаруудаар шалгана.

### CI (GitHub Actions)

| Workflow | Хэзээ | Юу хийдэг |
| --- | --- | --- |
| `ci.yml` | push, PR бүрт | gofmt, go vet, go test (Ubuntu + Windows), race detector |
| `e2e.yml` | Даваа гараг бүр 09:00 (Улаанбаатар), generator өөрчлөгдөхөд, гараар | 36 хослолыг entity-тэй болон entity-гүйгээр хамгийн сүүлийн хувилбараар шалгана. Үүссэн repository-г жинхэнэ PostgreSQL, MySQL дээр шалгана. 9 Java хослолыг entity-тэй болон entity-гүйгээр `mvnw verify`-ээр, Java-гийн JDBC repository-г жинхэнэ DB дээр шалгана. Эвдэрвэл issue нээнэ |
| Dependabot | 7 хоног бүр | projgen-ий Go dependency, Actions-ийн хувилбарыг шинэчлэх PR |
