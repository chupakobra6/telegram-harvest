# Карта Источников

Проект: telegram-harvest
Обновлено: 2026-07-30

## Приоритет Источников
1. Текущая прямая инструкция Игоря.
2. Применимые `AGENTS.md` и system/developer instructions.
3. Существующий код, тесты, схемы и канонические проектные документы.
4. Принятое состояние `.project-loop/`.
5. Предыдущий handoff.
6. Исходный intake и внешний research.
7. Вывод модели.

## Источники
| ID | Тип | Дата | Расположение | Статус | Примечания |
| --- | --- | --- | --- | --- | --- |
| S001 | user | 2026-07-30 | current request | принято | Полностью реализовать range-scan catch-up, протестировать и сравнить эффект со старым дневным обходом. |
| S002 | project rules | 2026-07-30 | `AGENTS.md`, `docs/catch-up.md` | принято | Read-only Telegram, последовательные paced RPC, daily scope, Trackmate/Haru и атомарные отчеты обязательны. |
| S003 | code | 2026-07-30 | `cmd/telegram-harvest/main.go`, `internal/mtproto/client.go` | принято | Текущий catch-up запускает отдельный Telegram scan для каждого дня. |
| S004 | user | 2026-07-30 | current request | принято | Добавить прямые stage timings для Telegram scan, download, ffmpeg, Vosk и render; хранить их независимо от перезаписываемых ASR-логов; сохранить единый range-scan. |

## Конфликты
| Источники | Решение | Дата |
| --- | --- | --- |
