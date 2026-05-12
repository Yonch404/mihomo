Place sing-box subcore assets here before building with the `with_singbox` tag.
The GitHub Actions workflow `.github/workflows/build-singbox.yml` fills this
directory automatically from the official sing-box release selected by
`SING_BOX_VERSION`.

Expected platform layout:

```text
assets/<goos>_<goarch>/sing-box
assets/<goos>_<goarch>/libcronet.so
assets/<goos>_<goarch>/libcronet.dylib
assets/<goos>_<goarch>/libcronet.dll
```

Windows builds should use `sing-box.exe`. A flat `assets/sing-box` layout is
accepted for one-off single-platform builds.
