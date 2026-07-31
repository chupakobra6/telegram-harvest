# Текущий Шаг

Проект: telegram-harvest
Обновлено: 2026-07-31

## Активный Шаг
- id: `STEP-009`
- status: `готово`
- objective: Ускорить основной недельный cold catch-up за счёт доказанно безопасного adaptive single-file download parallelism, более полной raw checkpoint boundary и переиспользуемого Makefile binary.
- requirement IDs: `REQ-024`—`REQ-026`, `VAL-017`—`VAL-019`, `CON-001`, `SCOPE-007`
- owned paths: `internal/mtproto`, `internal/harvest`, daily timing/report plumbing, checkpoint tests/contracts, `Makefile`, README/performance/catch-up docs и Project Loop state.
- validation: focused unit/failure tests; `make check`; `go test -race -count=1 ./...`; live 1/2/4/auto downloads с SHA-256/flood/error evidence; isolated full-vs-checkpoint JSONL comparison; Makefile reuse/rebuild smoke; `git diff --check`; Project Loop validation.
- done criteria: auto policy выбрана по live evidence и не меняет bytes; raw boundary не может пропустить self/Trackmate и откатывается к full scan при сомнении; повторный Make target не пересобирает binary; cold benchmark показывает честный эффект или недоказанная concurrency остаётся на 1.

## Фокус Ревью
- History и выбор media остаются у одного producer; одновременно активен максимум один file download.
- Chunk workers не выбираются по CPU/RAM: это сетевые workers, поэтому решение основано на размере файла и измеренном transport behavior.
- Raw checkpoint boundary считается только по реально прочитанным сообщениям до exclusive end и публикуется только после полного успешного catch-up.
- Не добавляются public tuning flags, альтернативные flow или новая ASR-конфигурация.

## Результат
- Production download auto policy: `<1 MiB → 1`, `>=1 MiB → 2`, hard cap 2; high-level producer и active file остаются одиночными.
- Большой corpus: fixed 2 примерно `1.92×` быстрее fixed 1 без FloodWait; fixed 4 отклонён после реального FloodWait; все SHA-256 совпали.
- Cold daily E2E: `46.885 → 40.095 s` (`−14.5%` total), download `24.029 → 16.399 s` (`−31.8%`), 270/270 records.
- Raw boundary учитывает все прочитанные сообщения до exclusive end; неподтверждённый head обнуляет dialog boundary и гарантирует следующий `MinID=0` fallback.
- Make переиспользует ignored binary и rebuild на content/add/delete source changes.
- `make check`, full race, Staticcheck, Govulncheck, Project Loop validation и independent rereview зелёные.
