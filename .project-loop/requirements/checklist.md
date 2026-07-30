# Чеклист Требований

Проект: telegram-harvest
Обновлено: 2026-07-30

## Значения Статусов
Используй `кандидат`, `принято`, `в работе`, `готово`, `отложено`, `заблокировано` или `отклонено`.

## Требования
| ID | Статус | Источник | Требование | Критерий приемки | Доказательства |
| --- | --- | --- | --- | --- | --- |
| REQ-001 | `готово` | S001, S003 | `daily-catchup` читает весь новый диапазон одним Telegram range-scan и затем разбивает записи по дневным отчетам. | Один вызов range collector на весь список новых jobs; каждый день получает только записи своего `[start,end)`. | `runDailyRangeJobs`; unit-тест single-call/partition/gaps; live run: один range, 70 batches, 8 дней. |
| REQ-002 | `готово` | S001, S002 | Новый flow сохраняет содержимое и формат daily: self, Trackmate только в Haru, форварды, медиа, транскрипты и merged catch-up. | Сравнение старого и нового результата на одинаковом диапазоне не показывает потери или лишние сообщения; существующие contract-тесты проходят. | Все 1 742 baseline records сохранены, 0 semantic mismatches, дополнительно найдены 22 исходящих сообщения; scope: 1 706 self/outgoing, 58 Trackmate, 0 other incoming. |
| REQ-003 | `готово` | S001 | Эффект измерен воспроизводимо на одном диапазоне. | Зафиксированы wall time, диапазон, количество дней/сообщений и отношение old/new; прогоны используют один профиль, ASR-кэш и одинаковые параметры. | 2026-07-22—2026-07-29: 290.77 s → 60.28 s, 4.82×; повторные range-runs 55.25 s и 54.74 s. |
| REQ-004 | `готово` | S004, S005 | Daily flow напрямую измеряет Telegram scan, media download, ffmpeg, Vosk и render. | Каждая стадия замеряется у места выполнения, включая failed work; timing report содержит wall total и stage work без восстановления данных из ASR JSONL; overlap описан отдельно. | `internal/stages`; observers в MTProto/transcribe/render; success/failure tests; pipeline span/overlap metrics. |
| REQ-005 | `готово` | S004 | Stage timings сохраняются как отдельный per-run structured artifact. | Успешный `daily`/`daily-catchup` печатает timings и атомарно сохраняет уникальный JSON report под `.state/daily/timings`; новый прогон не перезаписывает предыдущий. | `stage_timings.go`; unit test сохраняет два разных reports и проверяет первый; live report `20260730T074558.861993000Z-30261-1-daily-catchup.json`. |
| REQ-006 | `готово` | S005 | Daily media обрабатывается bounded pipeline без конкурентных Telegram RPC. | Telegram producer продолжает scan/download после enqueue; локальные jobs выполняются конкурентно; очередь ограничена и создает backpressure. | `media_pipeline.go`; queue backpressure test; live overlap 33.82–35.67 s при прежних 56 последовательных Telegram batches и 0 FloodWait. |
| REQ-007 | `готово` | S005 | Vosk workers независимы, а auto-controller выбирает безопасное эффективное число от 1 до 4. | Каждый worker имеет отдельный process/model/session; auto расширяется только при доказанной выгоде и достаточных ресурсах; diagnostic override поддерживает 1..4. | Auto resource tests; live auto выбрал 2, RSS 570–913 MiB/worker, fixed 4 не улучшил wall. |
| REQ-008 | `готово` | S005 | Кэш, dedup и collector не меняют содержимое или порядок отчетов. | Cache hit не создает job; одинаковый in-flight media выполняется один раз; atomic cache write; sequential и pipeline JSONL структурно совпадают. | Dedup/atomic/failure tests; normalized JSONL hash одинаков во всех 5 cold modes; Markdown SHA-256 одинаков без нормализации; temp files 0. |
| REQ-009 | `готово` | S005 | Timing report честно описывает перекрывающиеся стадии и ресурсы workers. | Есть pipeline span/overlap, requested/activated/peak workers, per-worker startup/RSS/jobs/audio/ffmpeg/Vosk/speed, queue peak; work-seconds не выдаются за wall decomposition. | `media_pipeline` timing object; stage work replaces invalid unaccounted arithmetic; live reports inspected for 1/2/4/auto and warm cache. |
| REQ-010 | `готово` | S006 | Транскрипция использует единый typed ASR backend contract с реализациями Vosk CPU и whisper.cpp. | Daily pipeline не знает деталей запуска движка; timing/cache identity включают backend, модель, вариант ускорения и существенные параметры декодирования. | `Descriptor`, `WorkerPolicy`, managed runners; cache изолирован по backend/binary/model/accelerator/language/threads/grammar/VAD; contract tests. |
| REQ-011 | `готово` | S006 | Worker policy зависит от backend: Vosk CPU может динамически масштабироваться, whisper.cpp Metal/Core ML в auto начинает и остаётся с одним GPU worker. | Auto не запускает конкурирующие GPU workers; явный diagnostic override отделён от безопасного default и виден в metrics. | Policy/pipeline tests; live timing: `worker_resource=gpu`, `dynamic_workers=false`, requested/activated/peak = 1. |
| REQ-012 | `готово` | S006 | Для whisper.cpp поддерживаются Metal и Metal + Core ML encoder, multilingual models и quantized model files без смешивания результатов. | Backend сообщает фактический accelerator/model identity; Core ML/Metal readiness проверяется; кэш одного варианта никогда не обслуживает другой. | Официальный whisper.cpp `4523d0ce`; Metal/Core ML builds; runtime checks подтвердили Apple M4 Pro, Metal device, `COREML=1` и `Core ML model loaded`; cache collision tests. |
| REQ-013 | `готово` | S006 | Один реальный локальный корпус сравнивает backend-варианты по производительности и качеству. | Для каждого варианта сохранены audio seconds, ASR/pipeline speed, cold-start, peak RSS, CPU и доступная GPU evidence, WER/CER, empty/error counts; одинаковые входы/reference и нормализация. | Corpus 170.284 s/hash `ba03ca…16c4`; 6 variants × 3 fresh processes; current JSON + documented table/transcripts; GPU percent explicitly unavailable without elevated powermetrics. |

## Ограничения
| ID | Статус | Источник | Ограничение | Доказательства |
| --- | --- | --- | --- | --- |
| CON-001 | `готово` | S002 | Telegram RPC остаются последовательными и используют существующий pacing 700 ms. | Range collector использует прежний последовательный MTProto loop и pacing; live run: 0 FloodWait. |
| CON-002 | `готово` | S002 | Нельзя расширять daily scope до остальных входящих сообщений Haru. | Структурная проверка: Trackmate 58, остальные incoming 0. |
| CON-003 | `готово` | S002 | Пользовательские report JSONL/Markdown публикуются атомарно; технические ASR-логи могут оставаться частичными при interruption; ZIP по умолчанию не создается. | Incomplete/error-path tests сохраняют старые reports; ASR error блокирует публикацию; ZIP не создавался. |

## Обязательная Валидация
| ID | Статус | Источник | Валидация | Доказательства |
| --- | --- | --- | --- | --- |
| VAL-001 | `готово` | S001 | Focused unit/integration tests для range partition, gaps, incomplete scan и ASR routing. | Добавлены тесты single-call, half-open boundaries, gaps, per-day limit, skipped media, incomplete publish, ASR routing и ASR error propagation. |
| VAL-002 | `готово` | S001, S002 | `gofmt`, `go test ./...`, `git diff --check`. | Все проверки зелёные на current head 2026-07-30. |
| VAL-003 | `готово` | S001 | Live old/new benchmark 2026-07-22—2026-07-29 и структурная сверка JSONL/Markdown. | 290.77 s baseline против 60.28 s range; final 54.74 s; 0 missing, 22 added, 0 common semantic mismatches; final Markdown идентичен предыдущему current-head run. |
| VAL-004 | `готово` | S004, S005 | Unit-тесты stage work/persistence и live `daily-catchup` с проверкой пяти полей timing report. | `go test ./...` зелёный; JSON содержит все stage fields, `stage_work_seconds`, wall total и отдельный pipeline object; live: 1 range, 70 batches, 1 764 records, 0 FloodWait. |
| VAL-005 | `готово` | S005 | Pipeline/controller/cache проходят focused, race, failure и repeated concurrency tests. | `go test ./...`, `go vet ./...`, `go test -race` и `TestMediaPipeline` ×20 зелёные; cancellation/error cleanup и bounded queue покрыты. |
| VAL-006 | `готово` | S005 | Cold-cache sequential/1/2/4/auto benchmark и structural equivalence. | 94.22 s sequential; 60.66 s fixed1; 54.56 s fixed2; 55.06 s fixed4; auto 54.95/55.61 s. 210 records, 3 ASR jobs, 170.284 s audio, 0 FloodWait, identical normalized JSONL/raw Markdown. |
| VAL-007 | `готово` | S006 | Backend/cache/policy/helper проходят unit, integration, race и failure tests. | `go test ./...`, `go vet ./...`, focused `-race`, `git diff --check` зелёные; fake long-lived server и cleanup/cache/policy tests. |
| VAL-008 | `готово` | S006 | Vosk, whisper.cpp Metal и whisper.cpp Core ML benchmark выполнен на одном реальном корпусе. | `.state/asr-benchmark/results-final-current.json`; 6 variants × 3, одинаковый corpus hash; cold/steady, performance/resources/quality и runtime evidence сохранены. |
| VAL-009 | `готово` | S006 | Daily catch-up с выбранным победителем сохраняет report contract и детерминизм. | Live turbo Metal: 54.383 s, 29.40× ASR, 16.69× pipeline, 1 worker, 0 FloodWait; 210 общих records, 0 semantic mismatches. Один прежний Telegram-search record отсутствует и воспроизведён также без ASR, поэтому явно отделён от backend validation. |

## Границы Объема
| ID | Статус | Источник | Граница | Примечания |
| --- | --- | --- | --- | --- |
| SCOPE-001 | `принято` | S001 | Параллельный ASR, смена движка и снижение RPC spacing не входили в STEP-001. | STEP-001 ограничивался устранением повторных дневных Telegram-сканов; параллельный ASR позже отдельно принят в STEP-003 через S005. |
| SCOPE-002 | `принято` | S005 | STEP-003 не меняет ASR engine и Telegram pacing. | Vosk сохраняется; Telegram RPC/download строго последовательны и paced. |
| SCOPE-003 | `принято` | S006 | STEP-004 не меняет Telegram scan/download pacing и не пытается ускорять Vosk через Metal. | Изменения ограничены общим ASR contract, локальными движками, backend-specific pool, измерениями и выбором default по результатам. |
