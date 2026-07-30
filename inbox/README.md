# Project Inbox

Этот inbox нужен для новых входящих материалов до продвижения в durable Project Loop state.

## Маршруты Intake

- Свежие комментарии, правки, решения и изменения области от Игоря: сохранить raw или точную выдержку в `.project-loop/intake/user-deltas.md`, затем обновить `source-map.md`, `checklist.md`, `delivery-plan.md`, `current-step.md` и `handoff.md`.
- Новые документы, PDF, research notes и большие источники: сохранить копию или точную выдержку в `.project-loop/intake/raw/`, затем зарегистрировать источник в `.project-loop/requirements/source-map.md`.
- Текстовые материалы intake сохранять как Markdown (`.md`). `.txt` не использовать для документов проекта; машинные данные сохранять с явным расширением по типу данных, например `.log`, `.csv` или `.json`.
- Project rules, которые должны пережить будущие сессии: зафиксировать как требование, ограничение, validation obligation или boundary в `.project-loop/requirements/checklist.md`.
- Комментарии про сам Project Loop skill/template: зафиксировать также в `/Users/igor/plugins/project-loop/skills/project-loop/inbox/project-loop-rules.md` и продвинуть в skill/template files.
- Sensitive values, credentials, cookies, payment data, addresses и private session state: хранить вне shared project files; в Project Loop переносить только non-sensitive normalized rule.

## Проверка Перед Handoff

- Все свежие комментарии Игоря имеют source entry.
- Все принятые изменения имеют checklist item или обновленный existing item.
- Handoff перечисляет, что было принято, куда продвинуто и что осталось next action.
