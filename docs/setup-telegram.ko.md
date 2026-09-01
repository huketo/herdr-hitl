# Telegram 설정

*[English](setup-telegram.md) · 한국어*

5분, 봇 하나, 토큰 하나. 질문은 인라인 버튼이 달린 메시지로 도착하고, 답은 버튼 탭이나 답장으로 돌아옵니다.

Telegram은 그룹도 채널도 필요 없습니다. 질문은 봇이 보내는 DM으로 옵니다. 사전 조건은 하나뿐입니다 — **사람이 봇에게 먼저 말을 걸어야 합니다.** 봇은 대화를 먼저 시작할 수 없어서, 말 걸지 않은 봇은 `403 Forbidden: bot can't initiate conversation with a user`로 실패합니다.

## 1. 봇 만들기

Telegram에서 [@BotFather](https://t.me/BotFather)에게 말을 겁니다.

```
/newbot
-> 표시 이름을 정합니다 (예: My HITL)
-> 사용자명을 정합니다. bot으로 끝나야 합니다 (예: my_hitl_bot)
```

BotFather가 `123456789:AAFxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx` 형태의 토큰을 줍니다. **이것이 봇의 비밀번호입니다.** 채팅에 붙여넣지 말고, 저장소에 커밋하지 마십시오.

선택이지만 해두면 좋습니다:

```
/setdescription
/setprivacy   -> 봇 선택 -> Disable    (그룹에 질문을 올릴 때만)
```

## 2. 자기 chat id 알아내기

봇에게 아무 메시지나 하나 보내십시오. `/start`면 됩니다. 그다음:

```sh
curl -s "https://api.telegram.org/bot123456789:AAFxxxx/getUpdates" | jq '.result[].message.chat'
```

```json
{ "id": 987654321, "first_name": "Huke", "type": "private" }
```

`987654321`이 당신의 `chat_id`입니다.

### 어떤 종류의 채팅을 고를 것인가

채팅 종류가 가능한 답의 형태를 결정합니다. **봇과의 개인 대화를 권장합니다.**

| 채팅 종류 | 버튼 | 글로 쓰는 답 | 답할 수 있는 사람 |
| --- | --- | --- | --- |
| 봇과의 개인 대화 | 됨 | 됨 | 당신만 |
| 그룹 / 슈퍼그룹 | 됨 | 됨 | 모든 멤버 — `allowed_user_ids`를 채우십시오 |
| **채널** | 됨 | **안 됨** | 모든 관리자 |

**채널은 방송 전용입니다.** 답장 상자가 없어서 글로 쓰는 답이 도착할 방법이 없습니다. Telegram은 답장 프롬프트를 `400 Bad Request: inline keyboard expected`로 아예 거절합니다. `herdr-hitl`은 시작할 때 이것을 감지해 프롬프트를 빼고, `-c` 선택지가 없는 질문은 아무도 답할 수 없으니 **보내는 대신 거절합니다.** `herdr-hitl doctor`가 알려줍니다:

```
OK  connection  telegram: @yourbot -> channel -1004434377702 (buttons only; free-text answers are impossible)
```

채널 id와 슈퍼그룹 id는 **둘 다 `-100`으로 시작해서 구분되지 않습니다.** `getChat`으로 확인하십시오:

```sh
curl -s "https://api.telegram.org/bot<TOKEN>/getChat?chat_id=-1004434377702" | jq -r '.result.type'
```

### 이 단계에서 흔히 막히는 네 가지

- **`result`가 빈 배열.** 봇에게 아직 말을 걸지 않았거나, 다른 무언가가 이미 그 업데이트를 소비했습니다 (`getUpdates`는 읽으면 사라집니다). 메시지를 하나 더 보내고 다시 시도하십시오.
- **`409 Conflict: terminated by other getUpdates request`.** 다른 poller가 토큰을 쥐고 있습니다. `curl` 전에 `herdr-hitl daemon stop`을 하십시오.
- **`409 Conflict: can't use getUpdates method while webhook is active`.** 이 토큰에 웹훅이 걸려 있습니다. `curl -s "https://api.telegram.org/bot<TOKEN>/deleteWebhook?drop_pending_updates=true"`로 지우십시오. `herdr-hitl`은 long polling만 씁니다 ([ADR-0002](adr/0002-messenger-transports-telegram-gateway-discord.md)). 웹훅과 long polling은 한 토큰에서 공존할 수 없습니다.
- **봇 자신의 id를 썼습니다.** `getMe`가 돌려주는 것은 **봇의** id입니다. `getChat`으로 조회하면 `type: private`이 나와서 그럴듯해 보입니다. 당신의 id는 당신이 보낸 메시지의 `from.id`입니다.

## 3. 설정 쓰기

```sh
herdr-hitl config init
herdr-hitl config path
```

파일을 열어 채우십시오:

```toml
transports = ["telegram"]

[telegram]
enabled = true
chat_id = "987654321"
allowed_user_ids = ["987654321"]
```

토큰은 `.env`에 넣습니다:

```sh
printf 'HITL_TELEGRAM_BOT_TOKEN=123456789:AAFxxxx\n' >> "$(herdr-hitl config path | xargs dirname)/.env"
chmod 600 "$(herdr-hitl config path | xargs dirname)/.env"
```

`.env`가 `config.toml`보다 우선합니다. 토큰이 설정 파일에 남지 않습니다.

### `allowed_user_ids`

`chat_id`는 질문이 *어디로* 갈지를, `allowed_user_ids`는 *누가 답할 수 있는지*를 정합니다. 비어 있지 않으면 다른 사용자 id의 버튼 탭이나 답장은 거부되고 질문은 계속 열려 있습니다. 비워두면 그 채팅에 접근할 수 있는 누구나 당신 대신 답할 수 있습니다 — 개인 대화에서는 괜찮지만 공유 그룹에서는 위험합니다. 최소한 자기 id는 넣으십시오.

## 4. 확인

```sh
herdr-hitl daemon restart
herdr-hitl doctor
```

## 5. 연결 시험

```sh
herdr-hitl ask \
  -t "연결 확인" \
  -m "버튼을 누르거나 아무 글이나 보내주세요." \
  -c "ok=잘 됩니다" -c "nope=문제가 있습니다" \
  --primary ok --danger nope --timeout 5m
```

`잘 됩니다`를 누르면 터미널에 `잘 됩니다`가 출력되고 `0`으로 끝납니다. Telegram 메시지는 그 자리에서 수정되어 버튼이 사라지고 결과가 표시됩니다. 답하지 않고 두면 `3`으로 끝납니다.

## 6. Daemon이 있는 이유

Telegram의 업데이트 큐는 **봇 토큰마다 하나뿐이고, 읽으면 사라집니다.** 두 프로세스가 같은 토큰을 폴링하면 서로의 답을 지웁니다. 새 long poll은 기존 것을 `409 Conflict: terminated by other getUpdates request`로 쫓아냅니다.

그래서 Daemon 하나가 토큰을 소유하고, 모든 `ask`가 그 위에 올라탑니다. Daemon은 수명 내내 락 파일을 붙들어 두 번째 Daemon이 뜨는 것을 막습니다. [ADR-0001](adr/0001-blocking-cli-over-a-resident-daemon.md)을 보십시오.

## 문제 해결

| 증상 | 원인 |
| --- | --- |
| `401 Unauthorized` | 토큰이 틀렸거나 취소되었습니다. BotFather에서 다시 확인하십시오. |
| `403 Forbidden: bot can't initiate conversation with a user` | 봇에게 말을 건 적이 없습니다. `/start`를 보내십시오. |
| `400 Bad Request: chat not found` | `chat_id`가 틀렸거나, 봇이 추방된 그룹의 id입니다. |
| `400 Bad Request: inline keyboard expected` | 대상이 채널입니다. 채널은 버튼만 받습니다 — `-c` 선택지를 주거나, `chat_id`를 개인 대화·그룹·슈퍼그룹으로 바꾸십시오. |
| `409 Conflict: terminated by other getUpdates request` | poller가 둘입니다. 하나를 멈추십시오. |
| `409 Conflict: can't use getUpdates method while webhook is active` | `deleteWebhook`을 실행하십시오. |
| 버튼을 눌러도 아무 일이 없음 | 당신의 사용자 id가 `allowed_user_ids`에 없습니다. |
| 메시지는 갔는데 답이 안 돌아옴 | Daemon이 도중에 죽었습니다. `herdr-hitl daemon status`, 그다음 로그를 보십시오. |
| 첨부가 거부됨 | 첨부는 질문당 10개, 개당 10 MiB가 상한입니다. |
