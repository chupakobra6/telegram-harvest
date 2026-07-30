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
| S005 | user | 2026-07-30 | `/Users/igor/.codex/attachments/2615f2f2-55ee-4d82-b3be-11a55ab1a100/pasted-text.txt` | принято | Полностью реализовать bounded media pipeline и консервативный динамический пул независимых Vosk workers, переиспользовать кэш, сохранить детерминированные отчеты, измерить ресурсы и сравнить sequential/1/2/4/auto. |
| S006 | user | 2026-07-30 | current request | принято | Не добавлять Metal в Vosk; ввести общий ASR backend, реализовать Vosk CPU и whisper.cpp Metal/Core ML, сделать backend-specific worker policy и воспроизводимо сравнить скорость, cold-start, RAM/CPU/GPU и качество русского текста на одном реальном корпусе. |

## Конфликты
| Источники | Решение | Дата |
| --- | --- | --- |
