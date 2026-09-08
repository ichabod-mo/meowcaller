# meowcaller
[![Go Reference](https://pkg.go.dev/badge/github.com/purpshell/meowcaller.svg)](https://pkg.go.dev/github.com/purpshell/meowcaller)

meowcaller is a Go library for the WhatsApp Web VoIP stack. It is 100% pure GO without CGO and it has minimal dependencies. It includes the novel proprietary audio codec MLOW written and validated completely in GO. In turn, meowcaller does not rely on any native bindings and can run everywhere that GO can.

## Discussion
Matrix room: [#meowcaller:matrix.org](https://matrix.to/#/#meowcaller:matrix.org).

Discord channel: #meowcaller in the [WhiskeySockets Discord server](https://whiskey.so/discord).

You can find the underlying spec in the [WhatsApp Calls Research Group](https://wacrg.org). Video transition behavior is cross-checked against the independently implemented [whatsapp-rust call stack](https://github.com/oxidezap/whatsapp-rust/pull/1024).

## Usage
The [godoc](https://pkg.go.dev/github.com/purpshell/meowcaller) includes docs for all methods.

There's a range of examples in the [examples](/examples/) directory.

### fork 兼容依赖

`ichabod-mo/meowcaller v1.0.2` 使用规范路径 `go.mau.fi/whatsmeow`，
与 `pcom-git/whatsmeow v1.1.7` 的 `Client`、JID 和账号存储保持一致。
业务代码仍导入 `github.com/purpshell/meowcaller`；在使用方主模块的
`go.mod` 中配置：

```go
require (
    github.com/purpshell/meowcaller v1.0.2
    go.mau.fi/whatsmeow v0.0.0-20260722203353-e9a033b24933
)

replace github.com/purpshell/meowcaller => github.com/ichabod-mo/meowcaller v1.0.2
replace go.mau.fi/whatsmeow => github.com/pcom-git/whatsmeow v1.1.7
replace go.mau.fi/util => go.mau.fi/util v0.9.10
```

依赖库内的 `replace` 不会传递到使用方，因此主模块必须保留上述配置；
`util v0.9.10` 用于兼容该 pcom 版本的存储 API。仓库内独立示例模块也使用相同配置。
此版本保留外呼 offer 失败清理和视频 RTP 时间戳／SSRC 透传，不修改通话逻辑或迁移账号库。
早期 Hypermeow 版本使用不同类型，不能与 pcom 的 `Client` 混用；
本兼容版本不代表将已有 Hypermeow 账号库迁移到 pcom。

The API is easy to approach and implement: attach a **`Source`** to send media, a **`Sink`** to receive it, and register callbacks for call events.

A 12-line example to show the power and simplicity of the library:
```go
// wa is a whatsmeow.Client
client := meowcaller.NewClient(wa)

client.OnIncomingCall(func(call *meowcaller.Call) {
    call.Answer()

    if mp3, err := meowcaller.MP3File("hello.mp3"); err == nil {
        call.Play(mp3)               // stream audio to the caller
    }
    if wav, err := meowcaller.WAVRecorder("caller.wav"); err == nil {
        call.Receive(wav)            // record their voice
    }
    if h264, err := meowcaller.AnnexBRecorder("caller.h264"); err == nil {
        call.ReceiveVideo(h264)      // record their video
    }
})

// Placing a call is just as short:
call, _ := client.Call(ctx, "+15551234567")
call.Receive(meowcaller.SinkFunc(func(pcm []float32) { /* the peer's audio */ }))
```

## Features

Core VoIP features are present:

- Outbound calls
- Inbound calls
- Audio calls (the pure-Go MLow codec)
- Video calls, including calls that start with video
- Mid-call audio-to-video upgrade, acceptance, rejection, cancellation, and downgrade
- Camera orientation and authenticated video keyframe feedback
- Send and receive call emoji reactions over the dedicated RTC app-data stream
- Experimental ad-hoc and group-bound group calls, including add/ring participant
- Experimental reusable call links and approval waiting rooms
- Experimental participant video/reactions, arbitrary emoji, hand state, and screen-share state

本 fork 的实验性群通话接口使用上述固定的 pcom 依赖组合；本次兼容发布不承诺群通话实机验证。
功能边界见 [group-call feature guide](docs/whatsapp-group-call-features.md)，
测试控制台位于 `examples/web`。

Things that are not yet implemented:

- Opus codec fallback for clients not using MLOW (in progress; testing edge cases)
- Scheduled-call event messages (the call link itself is supported)

## Credits

meowcaller relies heavily on primitives that are implemented in the [WhatsApp Calls Research Group](https://wacrg.org). I thank all the developers who have contributed to it.

The video transition lifecycle was validated against [whatsapp-rust PR #1024](https://github.com/oxidezap/whatsapp-rust/pull/1024), an independent implementation of the same protocol.

## Sponsoring and contribution
You may contribute to the maintenance of this library by sponsoring its maintainers on [GitHub](https://purpshell.dev/sponsor).

You may also submit pull requests and issues where relevant, given you follow the contributor [Code of Conduct](CODE_OF_CONDUCT.md).

## License

This repository follows the MIT license, as stated in the [LICENSE](/LICENSE) file
