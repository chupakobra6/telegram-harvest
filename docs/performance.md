# Производительность Telegram Harvest

## Daily catch-up range scan

`daily-catchup` читает весь новый диапазон одним последовательным Telegram range-scan и только после завершения разбивает записи по московским дням. Telegram RPC не параллелятся и сохраняют штатный pacing 700 ms.

Контрольный benchmark от 2026-07-30:

| Параметр | Дневной scan для каждого дня | Единый range-scan |
| --- | ---: | ---: |
| Диапазон | 2026-07-22—2026-07-29 | 2026-07-22—2026-07-29 |
| Дней | 8 | 8 |
| Wall time | 290.77 s | 60.28 s |
| Flood waits | 0 | 0 |
| Сообщений | 1 742 | 1 764 |

Фактическое ускорение — **4.82×**, wall time уменьшился на **79.3%**. Оба прогона использовали профиль `main`, одинаковые параметры, прогретый transcript cache и отдельные report directories.

Два повторных прогона range-scan на итоговом коде заняли 55.25 s и 54.74 s. Они подтвердили стабильный результат; для консервативного сравнения выше оставлен первый замер 60.28 s.

Range-scan сохранил все 1 742 пары `(chat_id, message_id)` из baseline и дополнительно нашёл 22 исходящих сообщения, пропущенных узкими дневными Telegram search-запросами. Старых записей, отсутствующих в новом результате, нет. После исключения изменяемых Telegram counters (`views`, `forwards`, `replies` и dialog summary fields) содержимое всех общих records совпало.

Проверка daily scope нового результата:

- self/outgoing: 1 706;
- Trackmate в Haru: 58;
- остальные incoming: 0.

## Как повторять benchmark

Baseline revision — `e810c83` (`feat: preserve forwarded message origins`). Чтобы не переключать рабочую ветку и не копировать credentials/session в другой worktree, старый код собирается отдельным бинарником, а оба бинарника запускаются из текущего project root. Так они используют один `.env`, session, state и transcript cache:

```bash
git worktree add --detach /tmp/telegram-harvest-old e810c83
(cd /tmp/telegram-harvest-old && go build -o /tmp/telegram-harvest-old-bin ./cmd/telegram-harvest)
go build -o /tmp/telegram-harvest-range-bin ./cmd/telegram-harvest

mkdir -p /tmp/telegram-harvest-baseline
/usr/bin/time -p /tmp/telegram-harvest-old-bin \
  --profile main \
  daily-catchup \
  --from 2026-07-22 \
  --report-dir /tmp/telegram-harvest-baseline

mkdir -p /tmp/telegram-harvest-baseline-jsonl
cp .state/daily/jsonl/2026-07-{22,23,24,25,26,27,28,29}.jsonl \
  /tmp/telegram-harvest-baseline-jsonl/

mkdir -p /tmp/telegram-harvest-range
/usr/bin/time -p /tmp/telegram-harvest-range-bin \
  --profile main \
  daily-catchup \
  --from 2026-07-22 \
  --report-dir /tmp/telegram-harvest-range
```

Оба запуска используют default flags: `dialog-limit=500`, `download-media=true`, `transcribe=true`, `transcribe-video=phone` и одинаковый прогретый `.state/daily/transcripts/cache`.

Структурная сверка начинается с идентичности сообщений:

```bash
for day in 22 23 24 25 26 27 28 29; do
  jq -r '[.chat.id,.message_id] | @tsv' \
    "/tmp/telegram-harvest-baseline-jsonl/2026-07-$day.jsonl" | sort > "/tmp/old-$day.keys"
  jq -r '[.chat.id,.message_id] | @tsv' \
    ".state/daily/jsonl/2026-07-$day.jsonl" | sort > "/tmp/new-$day.keys"
  comm -23 "/tmp/old-$day.keys" "/tmp/new-$day.keys"
done
```

Пустой вывод `comm -23` означает, что range-scan не потерял ни одной baseline-записи. Общие records дополнительно сравниваются после исключения изменяемых Telegram counters: dialog summary fields, `views`, `forwards` и `replies`. Эти counters могут изменяться между последовательными чтениями одного исторического сообщения и не являются расхождением range partition.
