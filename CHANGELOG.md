# Changelog

## [0.1.4](https://github.com/huketo/herdr-hitl/compare/v0.1.3...v0.1.4) (2026-09-01)


### Documentation

* show the bot profile at the top of both READMEs ([#17](https://github.com/huketo/herdr-hitl/issues/17)) ([2f042d4](https://github.com/huketo/herdr-hitl/commit/2f042d4cfdbe2b54de07fb198e2908cdb91afbad))

## [0.1.3](https://github.com/huketo/herdr-hitl/compare/v0.1.2...v0.1.3) (2026-09-01)


### Bug fixes

* **cli:** prefer the release version over a synthesised pseudo-version ([#15](https://github.com/huketo/herdr-hitl/issues/15)) ([d483389](https://github.com/huketo/herdr-hitl/commit/d483389085ca68d438c226f75a471346bf488716))

## [0.1.2](https://github.com/huketo/herdr-hitl/compare/v0.1.1...v0.1.2) (2026-09-01)


### Bug fixes

* **cli:** report a real version when the linker did not stamp one ([#13](https://github.com/huketo/herdr-hitl/issues/13)) ([af2b90b](https://github.com/huketo/herdr-hitl/commit/af2b90b373c4f2ac06cb004a3e2a329d94373555))

## [0.1.1](https://github.com/huketo/herdr-hitl/compare/v0.1.0...v0.1.1) (2026-09-01)


### Bug fixes

* **paths:** resolve one config and one socket whatever launched the binary ([#12](https://github.com/huketo/herdr-hitl/issues/12)) ([f52566a](https://github.com/huketo/herdr-hitl/commit/f52566a618f42268b09b24921d3f2ad3f382f8d6))


### Documentation

* fix the mermaid notes and stop the token placeholder tripping scanners ([#10](https://github.com/huketo/herdr-hitl/issues/10)) ([92e1cf0](https://github.com/huketo/herdr-hitl/commit/92e1cf0cdd2dcfd357552d6f56e2699893a90586))

## 0.1.0 (2026-09-01)


### Features

* **broker:** add the human-in-the-loop domain model and broker ([084afbb](https://github.com/huketo/herdr-hitl/commit/084afbb7b9523a52118439fdccac63774a95a2fc))
* **cli:** add the blocking ask CLI and daemon lifecycle commands ([cce9b98](https://github.com/huketo/herdr-hitl/commit/cce9b989ac7b28cc4815cfa861e429f845805bb1))
* **config:** load settings from config.toml, .env, and the environment ([ea7abf6](https://github.com/huketo/herdr-hitl/commit/ea7abf652f9984e15fff6b3c6fbc1211b33db9cd))
* **daemon:** host the broker and report state back into Herdr ([eb4c61f](https://github.com/huketo/herdr-hitl/commit/eb4c61f8b9988592db9419ae1ad69356de06f2da))
* **daemon:** report each live transport's description in status ([eff8243](https://github.com/huketo/herdr-hitl/commit/eff8243904e2ff156531f3eb646d34a7e91e1f03))
* **discord:** deliver questions over the Discord gateway ([ef0fa02](https://github.com/huketo/herdr-hitl/commit/ef0fa02d5ae291cb25159ad435154d29189a7156))
* **ipc:** add the daemon protocol over a unix socket and a named pipe ([0529d38](https://github.com/huketo/herdr-hitl/commit/0529d38d00ee53b22ad8fb14d3268acae91de486))
* **plugin:** add the Herdr plugin manifest and the agent skill ([a7c47a6](https://github.com/huketo/herdr-hitl/commit/a7c47a6de0852e6836f0eb1ec59180c15001ec5a))
* **telegram:** deliver questions over the Telegram Bot API ([cb59082](https://github.com/huketo/herdr-hitl/commit/cb5908202d6c2dee3d31122dd8c1c7c5f1696581))


### Bug fixes

* **cli:** explain why the daemon exited instead of printing a socket error ([23ebc27](https://github.com/huketo/herdr-hitl/commit/23ebc27a46a9700d9d6ed36ac0b3b2eeda147b0c))
* **cli:** fail startup fast with the daemon's own reason ([2db52c7](https://github.com/huketo/herdr-hitl/commit/2db52c74ec9f3885ab2409beea9f8fcb0cf1111e))
* **daemon:** hold a lifetime lock so only one daemon can run ([ee127c6](https://github.com/huketo/herdr-hitl/commit/ee127c6d82c955f9fa3a8455c9e7314f2c90b95b))
* **discord:** put the invite URL in the no-mutual-guilds error ([e74d6a1](https://github.com/huketo/herdr-hitl/commit/e74d6a148544476f9d2eb565ceb50e2695931db6))
* **telegram:** call deleteWebhook without a params struct ([5ddae18](https://github.com/huketo/herdr-hitl/commit/5ddae18465e9a8f1e6e840f0c2bff66e4d19b79c))
* **telegram:** match the reply markup to the chat kind ([0fe3ed3](https://github.com/huketo/herdr-hitl/commit/0fe3ed3c614adbda9fbf72c15f5c3fe1a90ec27f))


### Documentation

* add Korean translations of the README and setup guides ([16b5d23](https://github.com/huketo/herdr-hitl/commit/16b5d23b29e9cbe270bbcfb17a46089f50b3feab))
* add the README, messenger setup guides, glossary, and ADRs ([fb96318](https://github.com/huketo/herdr-hitl/commit/fb96318f055c615b8e75c87941d5c6525e780b8d))
* explain how each transport is actually reached ([2687ac9](https://github.com/huketo/herdr-hitl/commit/2687ac953ffec76232dbabf4abae64fdf1653779))
* **telegram:** warn that a channel cannot take typed answers ([91b197a](https://github.com/huketo/herdr-hitl/commit/91b197aac6b4eac3de6d8c9344223d57c7d44bb1))
