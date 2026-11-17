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
