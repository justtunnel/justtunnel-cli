# Windows: Running Worker Tunnels

> Task Scheduler XML schema reference: https://learn.microsoft.com/en-us/windows/win32/taskschd/task-scheduler-schema

`justtunnel worker install` is not supported on Windows at v1 launch. This doc covers the two supported patterns:

1. **Foreground** — run in a terminal. The terminal is the supervisor.
2. **Task Scheduler** — detached, survives logoff, restarts on failure.

## Prerequisites

You have already created the worker and set your auth token:

```cmd
justtunnel auth justtunnel_sk_live_...
justtunnel worker create my-worker
```

Both commands work on Windows. The local config is written to
`%USERPROFILE%\.justtunnel\workers\my-worker.json`.

## Option 1: Foreground Mode

Run the worker in any open terminal:

```cmd
justtunnel worker start my-worker
```

Expected output:

```
Worker my-worker starting — https://my-worker--acme.justtunnel.dev
[2026-05-04T12:00:00Z] tunnel connected
```

Logs stream to stdout. Use `Ctrl-C` to stop.

To tail logs in a separate terminal while the worker is running:

```cmd
justtunnel worker logs my-worker
```

Logs are also written to `%USERPROFILE%\.justtunnel\logs\worker-my-worker.log`.

**When to use:** development, short-lived sessions, or when you do not need the
worker to restart after a reboot.

## Option 2: Task Scheduler (Detached)

Task Scheduler runs the worker as your user, with automatic restart on failure
and optional start-at-logon. No admin rights required if your account has
logon-as-batch rights (the default for most Windows accounts).

### Step 1: Find the binary path

```powershell
where.exe justtunnel
# Example: C:\Users\matt\AppData\Local\justtunnel\justtunnel.exe
```

### Step 2: Create the XML file

Save the following as `worker-my-worker.xml`. Replace `{{NAME}}` with your
worker name and `{{BINARY_PATH}}` with the full path from Step 1.

```xml
<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4"
  xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">

  <RegistrationInfo>
    <Description>justtunnel worker: {{NAME}}</Description>
    <Version>1.0</Version>
  </RegistrationInfo>

  <Triggers>
    <!-- Starts when you log on. For boot-time start see the note below. -->
    <LogonTrigger>
      <Enabled>true</Enabled>
    </LogonTrigger>
  </Triggers>

  <Principals>
    <Principal id="Author">
      <!-- Runs as the current user; no elevation required. -->
      <GroupId>Users</GroupId>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>

  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RestartOnFailure>
      <!-- Restart every 5 minutes, up to 999 times. -->
      <Interval>PT5M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
  </Settings>

  <Actions>
    <Exec>
      <Command>{{BINARY_PATH}}</Command>
      <Arguments>worker start {{NAME}}</Arguments>
    </Exec>
  </Actions>

</Task>
```

### Step 3: Import the task

Open a Command Prompt (no elevation needed) and run:

```cmd
schtasks /create /tn JustTunnelWorker_{{NAME}} /xml worker-{{NAME}}.xml
```

To verify it registered:

```cmd
schtasks /query /tn JustTunnelWorker_{{NAME}} /fo list
```

### Step 4: Start it now (without logging off)

```cmd
schtasks /run /tn JustTunnelWorker_{{NAME}}
```

### Stop and remove

```cmd
rem Stop the running task
schtasks /end /tn JustTunnelWorker_{{NAME}}

rem Delete the task definition
schtasks /delete /tn JustTunnelWorker_{{NAME}} /f
```

## Logs on Windows

The worker runner writes structured logs to:

```
%USERPROFILE%\.justtunnel\logs\worker-<name>.log
```

This path uses `os.UserHomeDir()` internally, which resolves correctly on
Windows. You can tail the file with PowerShell:

```powershell
Get-Content "$env:USERPROFILE\.justtunnel\logs\worker-my-worker.log" -Wait
```

Or with the CLI:

```cmd
justtunnel worker logs my-worker
```

## Boot-time Start (Advanced)

The `LogonTrigger` above starts the worker when you log on but not at system
boot (before any user logs in). If you need the worker to run at boot:

1. Change `<LogonTrigger>` to `<BootTrigger>` in the XML.
2. Change `<RunLevel>` from `LeastPrivilege` to `HighestAvailable` — the task
   must run with elevation for boot triggers.
3. Re-import the task (`schtasks /delete` first, then `/create` again).

This is more involved and requires that your account has administrator rights.
For most developer workloads the logon trigger is sufficient.

## Limitations (v1)

- `justtunnel worker install` returns a friendly error on Windows and points
  here. Use the patterns above instead.
- The `LogonTrigger` requires a user logon. The worker does not run headlessly
  before login (see boot-time section above for the workaround).
- There is no automatic start if the task is imported before the binary is on
  `PATH` — verify with `where.exe justtunnel` first.

## What's Next

v1.1 may add a native Windows service installer using `sc.exe create` or a WiX
bundle, which would give boot-time, pre-login startup without manual Task
Scheduler configuration. This is a v1 scope limitation, not a permanent gap.
Until then, Task Scheduler is the recommended production pattern for Windows.
