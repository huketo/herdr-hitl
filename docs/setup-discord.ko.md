# Discord 설정

*[English](setup-discord.md) · 한국어*

15분, 애플리케이션 하나, 비공개 봇 토큰 하나. 질문은 진짜 Discord 버튼이 달린 메시지로 도착하고, 답은 버튼 인터랙션이나 모달로 돌아옵니다.

질문은 Telegram과 똑같이 봇이 보내는 DM으로 옵니다. 다만 Discord에는 Telegram에 없는 사전 조건이 하나 있습니다 — **봇은 자기와 같은 서버에 있는 사람에게만 DM할 수 있습니다.** 건너뛰면 `50278 Cannot send messages to this user due to having no mutual guilds`로 실패합니다.

그 서버는 **자격 증명 역할일 뿐 목적지가 아닙니다.** 질문이 거기 올라가지 않고 열어볼 일도 없습니다. 당신과 봇 둘뿐인 빈 서버로 충분합니다. 서버를 나가거나 봇을 추방하면 DM이 다시 깨집니다.

## 1. 애플리케이션과 봇 만들기

1. [Discord 개발자 포털](https://discord.com/developers/applications)에서 **New Application**. 이름을 정합니다 (예: `My HITL`).
2. **General Information**에서 **Application ID**를 복사합니다. 초대 URL에 필요합니다.
3. **Bot**으로 가서 **Reset Token** → 확인 → 토큰 복사. **Discord는 이 토큰을 한 번만 보여줍니다.** 놓치면 다시 Reset해야 하고, 이전 토큰은 무효가 됩니다.

## 2. Interactions Endpoint URL은 비워 두십시오

**General Information**에 **Interactions Endpoint URL** 칸이 있습니다. **비워 두십시오.**

여기에 값을 넣으면 애플리케이션이 HTTP 인터랙션 방식으로 영구히 바뀝니다. Discord가 버튼 클릭을 게이트웨이로 보내지 않고 그 URL로 POST합니다. `herdr-hitl`은 게이트웨이로 받으므로, 값이 설정되어 있으면 모든 버튼 클릭이 Daemon을 비켜 가고 **버튼을 눌러도 아무 일이 없어 보입니다.** 이미 설정되어 있다면 지우고 저장하십시오.

## 3. 봇을 비공개로 만들기

**Bot**에서 **Public Bot**을 **끄십시오.**

공개 봇은 Application ID를 아는 누구나 자기 서버에 추가할 수 있습니다. 그 id는 비밀이 아닙니다 — 모든 초대 URL의 `client_id`에 그대로 들어 있습니다. HITL 봇은 당신의 질문을 읽고 답을 나릅니다. 낯선 사람이 초대할 수 있을 이유가 없습니다. **Public Bot**을 끄면 애플리케이션 소유자만 초대할 수 있고, 같은 URL이 당신에게는 계속 동작합니다.

**Requires OAuth2 Code Grant**도 **꺼둔 채로** 두십시오. 켜면 초대가 리다이렉트 URI와 콜백 서버를 요구하는 완전한 authorization-code 교환으로 바뀝니다. `herdr-hitl`에는 둘 다 없고, 아래의 평범한 초대 URL이 즉시 깨집니다.

**OAuth2 > Redirects**에도 아무것도 넣을 필요가 없습니다. 봇 전용 초대는 리다이렉트를 쓰지 않습니다.

| 설정 | 값 | 이유 |
| --- | --- | --- |
| Public Bot | **끔** | 당신만 봇을 서버에 추가할 수 있습니다 |
| Requires OAuth2 Code Grant | **끔** | 콜백 서버 없이는 초대 URL이 실패합니다 |
| Interactions Endpoint URL | **비움** | 인터랙션을 게이트웨이로 유지합니다 |
| Message Content Intent | **끔** | 4단계를 보십시오 |
| OAuth2 리다이렉트 URI | 없음 | 봇 초대는 쓰지 않습니다 |

## 4. Message Content Intent — 거의 항상 꺼둡니다

**Bot > Privileged Gateway Intents**에 있습니다. **끄십시오.** 아마 필요 없습니다.

글로 쓰는 답을 허용한 질문에는 **Write answer…** 버튼이 붙습니다. 누르면 Discord 모달이 열립니다 — 4000자까지 되는 제대로 된 여러 줄 입력 상자입니다. 답은 인터랙션 안에 담겨 옵니다. **인터랙션은 intent 제한을 받지 않으므로** DM에서도 서버 채널에서도 특권 권한 없이 동작합니다.

이 intent가 사주는 것은 딱 하나입니다 — 버튼을 쓰지 않고 채널에 **일반 메시지**로 답하는 것.

| 질문이 가는 곳 | 버튼 | 모달 (Write answer…) | 일반 메시지 답장 |
| --- | --- | --- | --- |
| 봇과의 DM | 됨 | 됨 | 됨 (DM은 제한에서 면제) |
| 서버 채널, intent **끔** | 됨 | 됨 | 무시됨, 내용이 비어서 옵니다 |
| 서버 채널, intent **켬** | 됨 | 됨 | 됨 |

서버 채널에서의 일반 답장이 정말 필요할 때만 켜고, 그때는 설정에 `message_content_intent = true`도 넣으십시오. 포털에서만 켜면 아무 변화가 없고, 설정에서만 켜면 게이트웨이가 `Disallowed intent(s)`로 끊깁니다.

## 5. 봇 초대하기

`<APP_ID>`를 1단계의 Application ID로 바꿔서 여십시오:

```
https://discord.com/oauth2/authorize?client_id=<APP_ID>&scope=bot%20applications.commands&permissions=125952
```

`125952`는 `VIEW_CHANNEL | SEND_MESSAGES | EMBED_LINKS | ATTACH_FILES | READ_MESSAGE_HISTORY`입니다. 첨부와 함께 질문을 올리고, 나중에 결과를 보여주려 수정하는 데 필요한 최소 권한입니다.

**Public Bot**이 꺼져 있으면 이 URL은 애플리케이션 소유자로 로그인했을 때만 동작합니다. 다른 사람에게는 "This bot cannot be added to servers"가 뜹니다. 그게 목적입니다. 손으로 URL을 쓰기 싫으면 포털의 **OAuth2 > URL Generator**가 같은 URL을 만들어 줍니다 — `bot`과 `applications.commands`를 고르고 위 권한 다섯 개를 체크하십시오.

서버 하나로 고정하려면:

```
https://discord.com/oauth2/authorize?client_id=<APP_ID>&scope=bot%20applications.commands&permissions=125952&guild_id=<GUILD_ID>&disable_guild_select=true
```

**DM만 쓸 계획이어도 반드시 초대해야 합니다.** `herdr-hitl`은 `50278`을 만나면 이 초대 URL을 오류 메시지에 직접 담아 줍니다.

## 6. 채널 또는 사용자 id 복사하기

Discord 클라이언트에서 **설정 > 고급 > 개발자 모드**를 켜십시오. 채널 우클릭 → **채널 ID 복사**, 자기 이름 우클릭 → **사용자 ID 복사**. 둘 다 17–19자리 숫자입니다.

## 7. 설정 쓰기

```sh
herdr-hitl config init
herdr-hitl config path
```

DM으로 받기 — `channel_id`를 비웁니다:

```toml
transports = ["discord"]
timeout = "30m"

[discord]
enabled = true
user_id = "987654321098765432"
allowed_user_ids = ["987654321098765432"]
message_content_intent = false
```

채널로 받기:

```toml
[discord]
enabled = true
channel_id = "123456789012345678"
user_id = "987654321098765432"
allowed_user_ids = ["987654321098765432"]
message_content_intent = true   # 이 채널에서 일반 답장이 필요할 때만
```

토큰은 `.env`에 넣습니다:

```sh
printf 'HITL_DISCORD_BOT_TOKEN=여기에_토큰\n' >> "$(herdr-hitl config path | xargs dirname)/.env"
chmod 600 "$(herdr-hitl config path | xargs dirname)/.env"
```

### `allowed_user_ids`

`channel_id`/`user_id`는 질문이 *어디로* 갈지를, `allowed_user_ids`는 *누가 답할 수 있는지*를 정합니다. 비어 있지 않으면 다른 사람의 버튼 클릭이나 답장은 거부되고 질문은 계속 열려 있습니다. 공유 채널에서는 반드시 채우십시오.

## 8. 확인

```sh
herdr-hitl daemon restart
herdr-hitl doctor
```

`doctor`가 게이트웨이 세션을 열고, 봇 태그를 보고하고, 대상 채널이나 DM을 해석합니다. 토큰은 절대 출력하지 않습니다.

## 9. Daemon이 있는 이유

Discord는 게이트웨이 `IDENTIFY`를 **봇 토큰당 24시간에 1000회**로 제한합니다. 초과했을 때의 벌칙은 재시도 대기가 아니라 **봇 토큰 초기화**입니다. 봇이 오프라인이 되고, 포털에서 새 토큰을 복사해 다시 설정할 때까지 돌아오지 않습니다.

`ask`마다 프로세스를 하나씩 띄우면 질문마다 IDENTIFY를 한 번씩 하게 됩니다. 몇 분에 한 번 묻는 바쁜 에이전트는 하루가 되기 훨씬 전에 1000을 넘기고 봇을 망가뜨립니다. 그래서 Daemon이 수명 내내 게이트웨이 세션 하나만 유지하고 모든 `ask`가 그 위에 올라탑니다. [ADR-0001](adr/0001-blocking-cli-over-a-resident-daemon.md)을 보십시오.

## 10. 연결 시험

```sh
herdr-hitl ask \
  -t "연결 확인" \
  -m "버튼을 누르거나 Write answer 로 답해 주세요." \
  -c "ok=잘 됩니다" -c "nope=문제가 있습니다" \
  --primary ok --danger nope --timeout 5m
```

`잘 됩니다`를 누르면 터미널에 그 라벨이 출력되고 `0`으로 끝납니다. Discord 메시지는 그 자리에서 수정되어 버튼이 사라집니다. **Write answer…** 를 누르면 모달이 열리고, 거기 쓴 글이 답으로 돌아옵니다. 답하지 않고 두면 `3`으로 끝납니다.

## 문제 해결

| 증상 | 원인 |
| --- | --- |
| `50278 Cannot send messages to this user…` | 공유 서버가 없습니다. 당신이 속한 서버에 봇을 초대하십시오. |
| `50001 Missing Access` | 봇이 채널을 볼 수 없습니다. `permissions=125952`로 다시 초대하거나 채널 권한을 주십시오. |
| `50013 Missing Permissions` | 채널 권한 설정이 메시지 보내기·링크 첨부·파일 첨부를 막고 있습니다. |
| 버튼을 눌러도 아무 일이 없음 | Interactions Endpoint URL이 설정되어 있습니다. 지우십시오. |
| 초대 URL에서 `This bot cannot be added to servers` | Public Bot이 꺼져 있고 소유자로 로그인하지 않았습니다. 의도된 동작입니다. |
| 초대 URL이 오류 페이지로 가거나 리다이렉트 URI를 요구함 | Requires OAuth2 Code Grant가 켜져 있습니다. 끄십시오. |
| 채널의 일반 메시지 답장이 무시됨 | Message Content Intent가 꺼져 있거나 `message_content_intent = false`입니다. 둘 다 켜거나, 그냥 **Write answer…** 버튼을 쓰십시오 — 이쪽은 아무것도 필요 없습니다. |
| 연결 시 `Disallowed intent(s)` | `message_content_intent = true`인데 포털에서 intent가 꺼져 있습니다. |
| 봇이 오프라인이 되고 토큰이 안 먹음 | IDENTIFY 한도를 넘겨 Discord가 토큰을 초기화했습니다. 새 토큰을 복사하고, 토큰당 Daemon을 둘 이상 돌리지 마십시오. |
| 첨부가 거부됨 | 첨부는 질문당 10개, 개당 10 MiB가 상한입니다. |
