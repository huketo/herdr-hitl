# Telegram setup

*English · [한국어](setup-telegram.ko.md)*

Ten minutes, one bot, one numeric chat id. At the end you get questions as Telegram messages with inline buttons, and answers come back as button taps or plain replies.

Telegram needs no group and no channel. Questions arrive as a direct message from your bot. The one precondition is that **you message the bot first** — a bot cannot open a conversation with a person, so an unmessaged bot fails with `403 Forbidden: bot can't initiate conversation with a user`.

## 1. Create the bot

Open Telegram and message [@BotFather](https://t.me/BotFather):

```
/newbot
```

It asks for a display name (anything, e.g. `My HITL`) and a username that must end in `bot` (e.g. `myhitl_bot`). It answers with a token: a numeric bot id, a colon, then a 35-character secret. This guide writes it as `<BOT_TOKEN>` throughout — a realistic-looking example would be picked up by secret scanners, in this repository and in yours.

That token *is* the bot. Treat it as a password. If it leaks, `/revoke` in BotFather and take the new one.

Optional but worth it — turn off group privacy so nothing surprising happens later, and give the bot a description:

```
/setprivacy   -> select your bot -> Disable    (only if you post questions to a group)
/setdescription
```

## 2. Get your numeric chat id

**Message your bot first.** Telegram bots cannot initiate a conversation. Until you send the bot at least one message, it has no permission to DM you and every `sendMessage` fails with `403 Forbidden: bot can't initiate conversation with a user`. Open the bot's chat and send `/start`.

Now read the update queue:

```sh
curl -s "https://api.telegram.org/bot<BOT_TOKEN>/getUpdates" | jq '.result[].message.chat'
```

```json
{ "id": 987654321, "first_name": "Huke", "type": "private" }
```

`987654321` is your `chat_id`. For a group, add the bot to the group, post any message there, and take the group's `id` — it is negative, e.g. `-1001234567890`.

### Pick the right kind of chat

The chat kind decides which answers are possible. A private chat with the bot is the recommended target.

| Chat kind | Buttons | Typed answers | Who can answer |
| --- | --- | --- | --- |
| Private chat with the bot | yes | yes | only you |
| Group / supergroup | yes | yes | every member — set `allowed_user_ids` |
| **Channel** | yes | **no** | every admin |

A **channel is broadcast-only**: it has no reply box, so a typed answer can never arrive. Telegram rejects the reply prompt outright with `400 Bad Request: inline keyboard expected`. `herdr-hitl` detects this at startup, drops the prompt, and refuses a question that offers no `-c` choices rather than posting one nobody can answer. `herdr-hitl doctor` names it:

```
OK  connection  telegram: @yourbot -> channel -1004434377702 (buttons only; free-text answers are impossible)
```

Both channel ids and supergroup ids start with `-100`, so they look identical. Check with `getChat`:

```sh
curl -s "https://api.telegram.org/bot<TOKEN>/getChat?chat_id=-1004434377702" | jq -r '.result.type'
```

Four things break this step:

- **Empty `result` array.** Either you have not messaged the bot yet, or something already consumed the update (see step 5 — `getUpdates` is destructive). Send another message and retry.
- **`409 Conflict: terminated by other getUpdates request`.** Another poller holds the token. Stop the `herdr-hitl` daemon (`herdr-hitl daemon stop`) before running `curl`.
- **`409 Conflict: can't use getUpdates method while webhook is active`.** A webhook is set on this token. Clear it: `curl -s "https://api.telegram.org/bot<TOKEN>/deleteWebhook?drop_pending_updates=true"`. `herdr-hitl` uses long polling exclusively (see [ADR-0002](adr/0002-messenger-transports-telegram-gateway-discord.md)); a webhook and long polling cannot coexist on one token.
- **You used the bot's own id.** `getMe` returns the *bot's* id; `getChat` on it reports `type: private` and looks plausible. Your id is the `from.id` on a message you sent, not the bot's.

## 3. Write the config

```sh
herdr-hitl config init   # creates config.toml and prints where it went
herdr-hitl config path   # shows the config dir, config file, socket, and log paths
```

Open the config file it names and set:

```toml
transports = ["telegram"]
timeout = "30m"

[telegram]
enabled = true
chat_id = "987654321"
allowed_user_ids = ["987654321"]
```

Put the token in `.env`, not in `config.toml`:

```sh
cp .env.example "$(dirname "$(herdr-hitl config path)")/.env"
chmod 600 "$(dirname "$(herdr-hitl config path)")/.env"
$EDITOR "$(dirname "$(herdr-hitl config path)")/.env"   # fill HITL_TELEGRAM_BOT_TOKEN
```

Outside Herdr, `.env` goes in the config directory printed by `herdr-hitl config path`, or export `HITL_TELEGRAM_BOT_TOKEN` in your shell profile.

### `allowed_user_ids`

`chat_id` says *where* the question goes. `allowed_user_ids` says *who may answer it*. If the list is non-empty, a button tap or reply from any other Telegram user id is rejected and the question stays open. Leave it empty and anyone with access to that chat can answer on your behalf — fine for a private DM, dangerous for a shared group. Set it to at least your own id.

## 4. Verify

```sh
herdr-hitl daemon restart
herdr-hitl doctor
```

`doctor` resolves the config, connects to the Bot API, calls `getMe`, and reports the bot username and the resolved chat. It never prints the token. A failure here is a config or network problem, not an agent problem.

## 5. Why there is a daemon

The Telegram Bot API has exactly one update queue per bot token, and reading it **deletes** what it returns. `getUpdates` also refuses to run twice concurrently on the same token — the second caller gets `409 Conflict: terminated by other getUpdates request`, and the two pollers then fight, each stealing updates the other was waiting for. Answers vanish.

So only one process may ever poll a given bot token. `herdr-hitl` makes that process a resident daemon, and `herdr-hitl ask` a thin client that talks to it over a unix socket. This is why you must not run two daemons against one token, and why `curl …/getUpdates` while the daemon is running will steal your answer.

## 6. Smoke test

```sh
herdr-hitl ask \
  -t "Setup smoke test" \
  -m "Tap a button, or type anything as a reply." \
  -c "ok=Looks good" -c "nope=Something is off" \
  --primary ok --timeout 5m
```

The message should arrive on your phone within a second. Tap `Looks good`; the terminal prints `Looks good` and exits `0`, and the Telegram message updates in place to show the outcome with the buttons removed. Let it run out instead and the command exits `3`.

Free text works too — reply to the bot's message with any text and that text lands on stdout.

## Troubleshooting

| Symptom | Cause |
| --- | --- |
| `403 Forbidden: bot can't initiate conversation with a user` | You never messaged the bot. Send `/start`. |
| `400 Bad Request: chat not found` | Wrong `chat_id`, or the id belongs to a group the bot was removed from. |
| `409 Conflict: terminated by other getUpdates request` | A second poller — another daemon, or your own `curl`. Stop it. |
| `409 Conflict: can't use getUpdates method while webhook is active` | Run `deleteWebhook`. |
| Buttons tap but nothing happens | Your Telegram user id is not in `allowed_user_ids`. |
| `400 Bad Request: inline keyboard expected` | The target is a channel. Channels take buttons only — give the question `-c` choices, or point `chat_id` at a private chat, group, or supergroup. |
| Message arrives, answer never returns | The daemon died mid-ask. `herdr-hitl daemon status`, then check the log path from `herdr-hitl config path`. |
| Attachment rejected | Attachments are capped at 10 MiB and 10 per question. |
