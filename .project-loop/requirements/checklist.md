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

## Границы Объема
| ID | Статус | Источник | Граница | Примечания |
| --- | --- | --- | --- | --- |
| SCOPE-001 | `принято` | S001 | Параллельный ASR, смена движка и снижение RPC spacing не входят в этот шаг. | Только устранение повторных дневных Telegram-сканов. |
