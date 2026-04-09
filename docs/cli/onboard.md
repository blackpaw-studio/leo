# leo onboard

Guided first-time setup with prerequisite checks and intelligent detection.

## Usage

```bash
leo onboard
```

## Description

`leo onboard` is the recommended entry point for new users. It checks prerequisites, detects existing installations, and routes you to the appropriate action:

- **Fresh install** -- runs `leo setup`
- **Existing Leo install** -- offers reconfiguration options

## Flow

```
leo onboard
  ├── Check prerequisites (claude CLI, curl)
  ├── Detect existing installations
  │   ├── No existing install → leo setup
  │   └── Leo found → offer reconfiguration
  └── Reconfigure options:
      ├── Telegram settings
      ├── Scheduled tasks
      └── Full reconfiguration
```

## See Also

- [`leo setup`](setup.md) -- the setup wizard itself
