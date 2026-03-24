# gocdp

This is an implementation of the Chrome Devtools Protocol in Go with both a low level and a high level API, inspired by and featuring a similar API to the Python [nodriver](https://github.com/ultrafunkamsterdam/nodriver) library.

It is not a direct 1:1 port of nodriver, and does not contain any code copied directly from nodriver, but it uses the same general design and follows some of the same behaviors closely. For now, it's also licensed under the same AGPLv3 license, since the intent was not really to create a permissive version of nodriver, but just get similar functionality in Go. This may change in the future if it is possibly useful.

Note that this library currently contains a lot of barely tested or untested code. :) YMMV.

## Usage

By default, gocdp employs the same strategy as nodriver to find a usable build of Chrome/Chromium, but you can override it using configuration.

Try an example as follows:

```shell
go run ./example/mouse_drag
```

This will use a headless Chrome instance by default. You can disable this:

```shell
go run ./example/mouse_drag -headless=false
```
