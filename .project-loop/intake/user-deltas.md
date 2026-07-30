# Пользовательские Дельты

Проект: telegram-harvest
Обновлено: 2026-07-30

Этот файл хранит существенные свежие корректировки, решения и изменения области от пользователя, которые приходят во время шага. Записи компактны и пополняются до отражения в источнике правды и handoff.

## Записи

### Исправить воспроизводимый пропуск messages.search

ID источника: `S007`

Исходный ввод:

```text
«и че это поправить нет?»
```

Контекст: предыдущий live comparison показал, что `messages.search` стабильно не
возвращает исходящее сообщение `1221157785:415830`.

Нормализация:
- [x] требование: не оставлять известную потерю сообщения;
- [x] решение: daily использует только `messages.getHistory`, sender scope фильтруется локально;
- [x] валидация: повторить 2026-07-25 и получить все 211 baseline keys;
- [x] реализация и validation завершены;
- [x] handoff обновлен.

Конфликты:
- полнота имеет приоритет над прежней скоростью historical scan; Telegram RPC остаются последовательными и paced.

### Единый range-scan для catch-up

ID источника: `S001`

Исходный ввод:

```text
«Читать весь catch-up диапазон одним проходом и затем разбивать сообщения по дням — реализуй полностью, протестируй, проверь и сравни эффект».
```

Нормализация:
- [x] требование: заменить отдельные дневные Telegram scan на один range-scan;
- [x] валидация: полностью протестировать, проверить живой flow и сравнить wall time.

Маршрутизация:
- [x] исходный ввод сохранен или записан здесь
- [x] карта источников обновлена
- [x] чеклист обновлен
- [x] план и текущий шаг обновлены
- [x] реализация направлена в рабочий шаг
- [x] reviewer проверил
- [x] handoff обновлен

Конфликты:
- отсутствуют

### Общий ASR backend и Whisper на Apple Silicon

ID источника: `S006`

Исходный ввод:

```text
Не пытаться прикручивать Metal к Vosk. Сделать общий ASR backend и сравнить
Vosk CPU, whisper.cpp Metal и whisper.cpp Metal + Core ML encoder на одном
реальном наборе по скорости, ресурсам и качеству. Для Whisper начинать с
одного GPU worker; динамический пул должен быть backend-specific.
```

Нормализация:
- [x] требование: общий typed backend contract и cache fingerprint;
- [x] требование: Vosk CPU, whisper.cpp Metal и Metal + Core ML;
- [x] требование: backend-specific worker policy, safe Whisper auto = 1;
- [x] требование: одинаковый real corpus, cold/steady performance и quality metrics;
- [x] требование: проверить multilingual models/quantization при практической пользе;
- [x] реализация завершена;
- [x] live benchmark и анализ завершены;
- [x] handoff обновлен.

Конфликты:
- нет; S006 явно открывает смену ASR engine, ранее исключённую только из STEP-003.

### Media pipeline и динамический Vosk pool

ID источника: `S005`

Исходный ввод:

```text
Полностью реализовать описанный bounded pipeline:
один последовательный Telegram producer → bounded queue →
динамический пул независимых ffmpeg/Vosk workers → deterministic collector.
Протестировать, проанализировать результаты, исправить найденное и показать итог.
```

Нормализация:
- [x] требование: Telegram history и media downloads остаются строго последовательными;
- [x] требование: producer не ждет локальную транскрипцию и использует bounded queue с backpressure;
- [x] требование: каждый ASR worker владеет отдельным Vosk process/model/session;
- [x] требование: transcript cache переиспользуется, одинаковое in-flight media не распознается дважды, запись кэша атомарна;
- [x] требование: collector восстанавливает прежний детерминированный порядок независимо от порядка завершения jobs;
- [x] требование: auto starts at 1, grows conservatively from backlog/audio/startup/RSS/CPU/memory evidence, hard limit initially 4, no shrink except memory pressure;
- [x] требование: timings expose pipeline span/overlap, worker count/startup/RSS/utilization/queue and aggregate/per-worker ASR speed;
- [x] валидация: output equivalence, unit/race/failure tests and cold-cache sequential/1/2/4/auto comparison.

Маршрутизация:
- [x] карта источников обновлена
- [x] чеклист обновлен
- [x] план и текущий шаг обновлены
- [x] реализация завершена
- [x] валидация завершена
- [x] handoff обновлен

Конфликты:
- нет; `S005` явно расширяет прежнюю границу `SCOPE-001`, где параллельный ASR был отложен.

### Честные stage timings

ID источника: `S004`

Исходный ввод:

```text
«Добавить честные stage timings: Telegram scan, download, ffmpeg, Vosk, render. Сейчас ASR-логи перезаписываются новым прогоном, поэтому исходные показатели распознавания уже потеряны».
```

Нормализация:
- [x] требование: измерять пять непересекающихся стадий непосредственно во время запуска;
- [x] требование: сохранять отдельный структурированный run report, не зависящий от перезаписываемых дневных ASR-логов;
- [x] требование: сохранить один range-scan с разбивкой по московским дням;
- [x] валидация: unit-тесты, полный Go test suite и live catch-up с проверкой timing report.

Маршрутизация:
- [x] исходный ввод записан здесь
- [x] карта источников обновлена
- [x] чеклист обновлен
- [x] план и текущий шаг обновлены
- [x] реализация направлена в рабочий шаг
- [x] handoff обновлен

Конфликты:
- отсутствуют
