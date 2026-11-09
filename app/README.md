# App code

The Flutter app. See the repository's main `README.md` for what it is,
how to get the APK, and the layout. The app itself holds no VPN logic;
it is a client of the core's dashboard API on loopback, authorized by
the `X-Local-Token` header from `local-api.token` in the data folder.
