# Handoff

Проект: telegram-harvest
Обновлено: 2026-07-31

## Цель И Статус

- Завершён `STEP-009`: ускорен основной cold catch-up без изменения ASR quality, sender scope, report format, pacing floor или атомарной публикации.
- Закрыты `REQ-024`—`REQ-026`, `VAL-017`—`VAL-019`, `CON-001`, `SCOPE-007`.

## Реализовано

- Telegram downloader выбирает один chunk worker для файлов меньше 1 MiB и два от 1 MiB; product API не может включить 4 workers.
- Одновременно активен только один file download; history/high-level media producer остаётся последовательным.
- Timing JSON/CLI сохраняет policy, bytes, transfer seconds/throughput, decisions, failures, retries, downloader FloodWait и download-specific transport floods; terminal errors тоже классифицируются.
- Успешный download дополнительно проверяет фактический file size против Telegram metadata.
- Checkpoint `verified_message_id` теперь учитывает все raw history messages строго до exclusive end до sender/report filter.
- Incomplete scan ничего не применяет; lower/missing-date raw head считается anomaly, boundary dialog сбрасывается в 0 и следующий run использует full per-dialog scan.
- Make targets используют ignored `bin/telegram-harvest`; source-set stamp отслеживает content/add/delete Go changes, `go.mod` и `go.sum`.

## Live Evidence

- Большой corpus: 5 immutable Telegram media, 124 401 837 bytes, 6–64 MiB; SHA-256 каждого файла одинаков во всех вариантах.
- Fixed 1: 76.887 s / 1.54 MiB/s / 0 FloodWait.
- Fixed 2: 40.033 s и repeat 38.684 s / 0 FloodWait; примерно `1.92×` быстрее baseline.
- Fixed 4: 22.983 s, но 1 real FloodWait/retry — отклонён.
- Final auto cap2: 35.747 s / 3.32 MiB/s / 0 retry/FloodWait/error.
- Threshold corpus 0.3–3.8 MiB: fixed1 6.471 s, fixed2 3.954 s, одинаковые hashes, 0 FloodWait; выбран threshold 1 MiB.
- Isolated cold daily 2026-07-30: 270 records, 15 downloads, 27 892 564 expected=transferred bytes, decisions `1:9 / 2:6`, 0 failures/retries/floods.
- Fixed1→auto: total `46.885→40.095 s` (`−14.5%`), download `24.029→16.399 s` (`−31.8%`), pipeline span `35.982→29.537 s` (`−17.9%`).
- Checkpoint A/B: 270/270 keys, 7/7 Trackmate, одинаковый stable payload, 0 FloodWait. Full/legacy/raw wall `25.248/25.959/25.172 s`; ускорение boundary на одном дне не заявляется.

## Проверка

- Focused checkpoint/download/timing tests — зелёные.
- `make check` — зелёный.
- `go test -race -count=1 ./...` — зелёный.
- Staticcheck — clean; Govulncheck — 0 reachable vulnerabilities.
- Make first/no-op/content/add/delete rebuild smoke — зелёный.
- `git diff --check` и Project Loop validation — зелёные.
- Independent reviewer сначала нашёл 4 edge case; после targeted repairs final verdict: `принято`, findings отсутствуют.

## Остаточные Риски

- Integrated cold comparison — один насыщенный день, а не полный weekly run; отдельный 124 MB corpus покрывает главный download reserve. Недельный эффект зависит от числа и размеров новых media.
- Exact приватные per-file hashes не коммитятся; tracked docs сохраняют агрегат и факт equality, private harness удаляется.
- Raw boundary не выполняет постоянный full shadow audit, потому что это добавило бы Telegram RPC. Безопасность обеспечивают conservative construction, anomaly fallback, complete/publish gates, tests и isolated live A/B.

## Следующее Действие

- Обязательной следующей работы нет; обычный `make daily-catchup PROFILE=main PROGRESS=1` автоматически использует новый binary/download/checkpoint flow.
