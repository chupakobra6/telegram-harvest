# Текущий Шаг

Проект: telegram-harvest
Обновлено: 2026-07-31

## Активный Шаг
- id: `STEP-008`
- status: `готово`
- objective: Проверить correctness и архитектуру Telegram Harvest, закрыть безопасные findings и довести repository DX/validation до законченного состояния.
- requirement IDs: `REQ-021`—`REQ-023`, `VAL-016`, `SCOPE-006`
- owned paths: Go module/dependencies, `Makefile`, `.github/workflows/`, CLI help, README/AGENTS, ignore policy и минимальные локальные cleanup fixes.
- validation: `make check`; `make audit`; `go test -race -count=1 ./...`; help/setup smoke; `git diff --check`; Project Loop validation.
- done criteria: static/security findings закрыты; module graph tidy; setup/check/audit/CI согласованы; state-dir path contract однозначен; product behavior не изменён.

## Фокус Ревью
- Telegram transport остаётся единственным последовательным paced producer.
- Daily checkpoint, ASR pipeline, report publication и sender scope не меняются.
- Исправляются фактические defects и friction, а не добавляются новые abstraction layers.
- Крупные `main.go`/`client.go` оцениваются как организационный долг; механический split без снижения coupling не входит в polish.

## Результат
- Staticcheck finding `S1023` исправлен.
- Минимальный toolchain поднят до Go 1.26.5, `x/net` до 0.53.0; Govulncheck: 0 reachable vulnerabilities.
- Добавлены `make check`, `make audit` и GitHub CI с full/race/static/security validation.
- `make setup` теперь только скачивает pinned dependencies и не мутирует `go.mod`/`go.sum`.
- Help/README/AGENTS синхронизированы с реальным state-dir-relative path contract; `.state/.state` guidance удалён.
- Private default artifacts дополнительно защищены `.gitignore`.
