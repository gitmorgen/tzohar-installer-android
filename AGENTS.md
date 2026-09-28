# Agent notes — tzohar-installer-android

Windows helper that sideloads + sets up the Tzohar Android agent on a
plugged-in phone over ADB. Go, no external dependencies (stdlib only), one
self-contained `.exe`.

## Build / deploy

- **No Windows build path required** (this laptop's SAC blocks Rust/cargo and
  running unsigned exes; there is no .NET host). It cross-compiles with Go:
  `GOOS=windows GOARCH=amd64 CGO_ENABLED=0`. Build on the Linux build box
  (`tzohar-specs/infra.md` §4) — the same box release.sh uses — or any machine
  with Go.
- `scripts/build.sh` → the exe. `scripts/deploy.sh [--prebuilt FILE.exe]`
  publishes to `tzohar-prod:/root/tzohar/backend/downloads/` as the stable
  alias `tzohar-installer-android.exe` (+ a versioned copy). `.exe` is already
  an allowed download extension in `tzohar-backend/src/routes/downloads.js`.
- Bump `VERSION` when you cut a release; it's stamped into the binary and the
  versioned filename.

## How it hangs together (cross-repo)

- **Backend**: no code needed. `GET /install/:id` already returns
  `install_key` + `artefact{url,sha256,version}` for an Android enrollment
  (`enrollmentStore.present` → `installCopy.artefactFor("android")`). The
  installer reads that; it does not parse manifests.
- **Android app** (`tzohar-app-android`): the zero-tap finish relies on
  `OnboardingActivity` auto-saving the key + starting the agent when launched
  via the `tzohar://install?key=…` deep link (agent **0.1.15 / versionCode 16**
  and later). Older builds still work — the deep link just prefills the key for
  a two-tap finish.

## Gotchas

- Package + service the installer addresses: `com.tzohar.agent` /
  `com.tzohar.agent.ScreenReaderService`. If either is renamed in the Android
  repo, update `main.go` (`pkg`, `accService`).
- `adb install -r -g` (not a file-manager sideload) is what keeps the app off
  Android 13+'s "restricted settings" list, so the accessibility toggle we set
  isn't greyed out. Keep the `-g`.
- The install-key deep link must be sent as a **single quoted device-shell
  line** (`ShellLine`), or the device's `sh` globs the `?` in the URI.
- The installer is Authenticode-signed with the **Tzohar publisher cert**, the
  same `tzohar-app-windows/scripts/sign.ps1` + `TZOHAR_SIGN_*` env the agent
  uses; `release.sh`'s `build_installer` signs it on the laptop after the
  cross-compile (Set-AuthenticodeSignature needs no SDK and doesn't run the exe,
  so SAC doesn't block signing). Self-signed → still an "unknown publisher"
  SmartScreen warning until the tech trusts the cert, and never passes SAC. The
  site's Windows-install steps carry the same trust command.
- Set git identity repo-locally (`gitmorgen`) — new repos start with none.
