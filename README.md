# DroidUtils

Requirements:

- JDK 25, Android SDK 37, Go 1.24+, Bash
- Android 12+; ARM64 userdebug system with rooted ADB

```sh
bash tools/build-service.sh
```

```powershell
.\gradlew.bat :app:assembleDebug :app:lintDebug
.\tools\install.ps1 -Serial <serial> -SkipBuild
```

```sh
cd service
go test ./...
```

[Optional phone-server integration](https://github.com/krashkanter/kela-server)

### Updates on merlinx

Pair the app with the matching ARM64 root daemon, open **Updates**, and tap **Check**.
Review the proposed version or package list before confirming an update.

- Debian uses the configured chroot repositories and applies in-place package updates. Restart the phone afterward to reload the running services.
- Hermes, nginx and cloudflared use the existing Kela Server Compose projects. The updater pins the selected image digest and retains the storage mounts. It attempts to restore the previous image if the updated service fails to start.
- Brave uses the official ARM64 APK and verifies its published checksum. Other listed apps may require their existing store or publisher channel.
- LineageOS checks the official merlinx build feed. A ROM upgrade requires matching custom boot and system_ext images and a separate recovery workflow.

The release APK is a debug build. Updating an existing DroidUtils installation requires the same signing certificate.
