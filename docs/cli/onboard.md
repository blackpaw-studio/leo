# leo onboard

Guided first-time setup with prerequisite checks and intelligent detection.

## Usage

```bash
leo onboard
```

## Description

`leo onboard` is the recommended entry point for new users. It checks prerequisites, detects existing installations, and routes you to the appropriate action:

- **Fresh install** — runs `leo setup`
- **OpenClaw detected** — offers to run `leo migrate`
- **Existing Leo install** — offers reconfiguration options

## Flow

```
leo onboard
  ├── Check prerequisites (claude CLI, curl)
  ├── Detect existing installations
  │   ├── No existing install → leo setup
  │   ├── OpenClaw found → offer leo migrate
  │   └── Leo found → offer reconfiguration
  └── Reconfigure options:
      ├── Telegram settings
      ├── Scheduled tasks
      └── Full reconfiguration
```

## See Also

- [`leo setup`](setup.md) — the setup wizard itself
- [`leo migrate`](migrate.md) — OpenClaw migration
