# Push this repo and get the APK

The repository is `https://github.com/meiyou313/Namaki` (branch `main`).

## 1. Push from your computer

In a terminal, inside this folder:

```bash
git init -b main
git add -A
git commit -m "Namaki 3.1.0"
git remote add origin https://github.com/meiyou313/Namaki.git
git push -u origin main
```

(If GitHub asks for a password, use a personal access token, not
your account password.)

## 2. The build runs itself

Every push to `main` starts the **Build Android** workflow
(`.github/workflows/build-android.yml`). The first run takes roughly
15–25 minutes (it downloads Go, Flutter, the Android SDK and the NDK,
and compiles the Go core).

When it is green, the workflow publishes the release (tag `v3.1.0`)
with the APKs, and also uploads them as the **android** artifact of
the run. Install the `arm64-v8a` APK on most phones.

## 3. If the run fails

The log names the failing step. Fix that one thing and push again —
each new push cancels the previous run, so only the latest run matters.
