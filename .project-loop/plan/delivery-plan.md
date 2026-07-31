# План Поставки

Проект: telegram-harvest
Обновлено: 2026-07-31

## Этапы
| Шаг | Статус | ID требований | Цель | Ревью | Проверка |
| --- | --- | --- | --- | --- | --- |
| STEP-001 | `готово` | REQ-001, REQ-002, REQ-003, VAL-001, VAL-002, VAL-003 | Реализовать единый range-scan, покрыть тестами и измерить old/new. | Проверены границы дней, фильтрация scope, атомарность и доказательства benchmark. | Focused tests, `go test ./...`, live old/new benchmark, structured comparison — зелёные. |
| STEP-001R | `готово` | REQ-001, REQ-002, REQ-003 | Независимо проверить diff и validation evidence, затем закрыть замечания. | Reviewer проверил итог после двух циклов исправлений. | Итоговый verdict: accepted, findings отсутствуют. |
| STEP-002 | `готово` | REQ-004, REQ-005, VAL-004 | Добавить прямые stage timings и неизменяемый per-run timing report, не нарушив единый range-scan. | Проверены границы стадий, отсутствие двойного учета, failure paths и атомарность report. | Focused tests, `go test ./...`, live catch-up и JSON report inspection — зелёные. |
| STEP-003 | `готово` | REQ-006—REQ-009, VAL-005, VAL-006, SCOPE-002 | Реализовать bounded media pipeline, независимые Vosk workers, auto-controller и расширенные overlapping-stage metrics. | Проверены последовательность Telegram, backpressure, dedup/cache atomicity, deterministic output и разумность scale-up; поздний auto scale исправлен после первого benchmark. | Focused/race/failure tests, full suite, structural equivalence, cold-cache sequential/1/2/4/auto benchmark — зелёные. |
| STEP-004 | `готово` | REQ-010—REQ-013, VAL-007—VAL-009, SCOPE-003 | Ввести общий ASR backend, добавить whisper.cpp Metal/Core ML, backend-specific worker policy и воспроизводимый performance/quality benchmark. | Проверены contract/cache isolation, фактическая Metal/Core ML activation, resource evidence, русские transcripts и VAD trade-off; выбран quality-first turbo Metal профиль. | Unit/race/failure tests, 6 variants × 3 на одном real corpus, live daily comparison, full suite — зелёные. |
| STEP-005 | `готово` | REQ-014, VAL-010 | Убрать неполный `messages.search` из daily и доказать полноту history-only scan. | Проверены один source of truth, локальный sender scope, границы/MinID, live 211/211 и честная цена по времени. | Focused/full/race/vet tests, no-ASR и full Whisper live 2026-07-25, structural comparison — зелёные. |
| STEP-006 | `готово` | REQ-015—REQ-017, VAL-011—VAL-013, SCOPE-004 | Расширить приватный Telegram speech corpus, исследовать и проверить decoder/hallucination настройки, выбрать один production ASR-профиль. | Проверены репрезентативность, content-first quality, critical deletions, non-speech false positives, cache identity и отсутствие лишних production profiles. | 42-media corpus; 10-way tuning + full matrix; 3 final repeats; 0/30 misses и 0/12 hallucinations; full/race/vet/pipeline×20; isolated live daily 211/211, 0 FloodWait. |
| STEP-007 | `готово` | REQ-018—REQ-020, VAL-014—VAL-015, CON-001, SCOPE-005 | Доказанно убрать лишний checkpoint history proof RPC и выбрать статический безопасный RPC floor. | Проверены exact response/head/MinID proof, все fallback paths, shadow rejection, metrics, pacing scheduler и отсутствие semantic потерь. | Full/race/vet; live 4/4 shadow proof; enforced byte-identical; 700→400 main matrix; единый 500 ms default; integrated CLI 211/211, 0 FloodWait. |
| STEP-008 | `готово` | REQ-021—REQ-023, VAL-016, SCOPE-006 | Провести tooling review и repo polish без изменения product contracts. | Проверены architecture boundaries, code duplication, defaults, setup/help/docs, private artifact policy, dependencies и CI. | `make check`, pinned staticcheck/govulncheck, full race, help smoke; 0 reachable vulnerabilities/findings. |

## Примечания По Порядку
- Шаги достаточно маленькие для цикла: реализация, ревью, исправление, проверка, коммит, handoff.
- Активен один шаг; непрерывное выполнение появляется только по явной инструкции Игоря.
- Для существенной работы используются пары `STEP-N` / `STEP-NR`.
- Человекочитаемые проектные артефакты пишутся на русском.
- Имена файлов описательные; ID источников хранятся в карте источников и чеклисте.
