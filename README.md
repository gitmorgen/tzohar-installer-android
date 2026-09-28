# tzohar-installer-android

A Windows helper that sets up the **Tzohar Android agent** on a plugged-in
phone with as few taps as possible. It's a single self-contained `.exe` that
opens a small step-by-step wizard in the browser and drives everything over
ADB.

## What it does

1. **Enrollment** — paste the enrollment link from the dashboard (or the
   install key). The installer fetches the current APK and the install key from
   `GET /install/:id`.
2. **Connect the phone** — you turn on USB debugging and accept the on-phone
   prompt (Android won't let software do these two). The wizard detects the
   phone live and tells you exactly what to tap.
3. **Install & set up** — automatically, over ADB:
   - downloads the latest APK (only if newer than the cached copy) and
     `adb install -r -g`;
   - enables the accessibility service (`enabled_accessibility_services`) —
     the reboot-safe screen-capture path on Android 11+;
   - exempts the app from battery optimization (`deviceidle whitelist`);
   - allows the app to install its own updates (`appops REQUEST_INSTALL_PACKAGES`);
   - fires the `tzohar://install?key=…` deep link, which on agent **0.1.15+**
     saves the key and starts the agent with no further taps.

`adb` (Google's platform-tools) is downloaded from Google on first run into
`%LOCALAPPDATA%\Tzohar\installer\` — it is not bundled (licensing).

## Build & publish

No Windows build path is needed: it cross-compiles from any box with Go
(the Linux build box — see `tzohar-specs/infra.md` §4).

```bash
scripts/build.sh                      # -> tzohar-installer-android.exe (windows/amd64)
scripts/deploy.sh                     # build here, then publish to the backend
scripts/deploy.sh --prebuilt X.exe    # publish an exe built elsewhere
```

`deploy.sh` uploads the stable alias `tzohar-installer-android.exe` (what people
download) and a versioned copy to the backend `downloads/`.

## Notes / limitations

- The `.exe` is Authenticode-signed with the **Tzohar publisher certificate** —
  the same self-signed cert and the same `sign.ps1` the Windows agent uses
  (`release.sh` signs it on the laptop after cross-compiling). Technicians trust
  that publisher once per PC via the command on the site's Windows-install
  steps; until they do, Windows still shows an "unknown publisher" SmartScreen
  warning (the cert is self-signed, so it earns no Microsoft reputation, and it
  does not satisfy Smart App Control). A CA-issued cert is the real fix.
- USB debugging + the RSA prompt are irreducibly manual — Android's security
  model. The wizard guides and detects them but cannot skip them.
- Android 10 has no accessibility screen-capture path, so capture there still
  needs one MediaProjection tap in the app after setup.

Run with `-api https://staging…` to point at a non-production backend, or
`-no-open` to not launch the browser.
