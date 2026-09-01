# Discord setup

Fifteen minutes, one application, one bot token. Questions arrive as messages with real Discord buttons; answers come back as button interactions or plain replies.

## 1. Create the application and bot

1. Open the [Discord developer portal](https://discord.com/developers/applications) and click **New Application**. Name it (e.g. `My HITL`).
2. Copy the **Application ID** from **General Information** — you need it for the invite URL.
3. Go to **Bot**. Click **Reset Token**, confirm, and copy the token. Discord shows it exactly once; if you lose it, reset again (which invalidates the old one).

## 2. Leave the Interactions Endpoint URL blank

Still under **General Information**, there is an **Interactions Endpoint URL** field. **Leave it empty.**

Setting it flips your application to HTTP interactions permanently: Discord stops delivering button clicks over the gateway and POSTs them to your URL instead, and it validates the URL with a signed PING before accepting it. `herdr-hitl` receives interactions over the gateway, so a configured endpoint diverts every button click away from the daemon and buttons appear to do nothing. If someone already set it, clear the field and save.

## 3. Message Content intent — usually off

Under **Bot > Privileged Gateway Intents** there is **Message Content Intent**.

- **Buttons only, or DM delivery:** leave it **off**. Button interactions carry their payload in the interaction itself, and DM channels with your bot are exempt from the message-content restriction — free-text replies in a DM arrive with their content regardless.
- **Free-text replies in a guild channel:** turn it **on**, and set `message_content_intent = true` in the config so the daemon requests it. Without both, message events in a guild channel arrive with an empty `content` and free-text answers silently never resolve.

Prefer DM delivery and keep the intent off. It is the smaller blast radius.

## 4. Invite the bot

Replace `<APP_ID>` with the Application ID from step 1 and open the URL:

```
https://discord.com/oauth2/authorize?client_id=<APP_ID>&scope=bot%20applications.commands&permissions=125952
```

`125952` is `VIEW_CHANNEL | SEND_MESSAGES | EMBED_LINKS | ATTACH_FILES | READ_MESSAGE_HISTORY` — the minimum for posting a question with attachments and editing it afterwards to show the outcome.

You must invite the bot to a guild you share **even if you only want DMs**. Discord refuses a bot-to-user DM when there is no mutual guild, with `50278: Cannot send a message to this user due to missing mutual guilds or friendship`. A private server with just you and the bot is enough.

## 5. Copy a channel or user id

Enable **Settings > Advanced > Developer Mode** in the Discord client. Right-click any channel and choose **Copy Channel ID**; right-click yourself and choose **Copy User ID**. Both are 17–19 digit snowflakes.

## 6. Write the config

```sh
herdr-hitl config init   # creates config.toml and prints where it went
herdr-hitl config path   # shows the config dir, config file, socket, and log paths
```

DM delivery — leave `channel_id` blank:

```toml
transports = ["discord"]
timeout = "30m"

[discord]
enabled = true
user_id = "987654321098765432"
allowed_user_ids = ["987654321098765432"]
message_content_intent = false
```

Channel delivery — `channel_id` set, `user_id` used to @-mention you:

```toml
[discord]
enabled = true
channel_id = "123456789012345678"
user_id = "987654321098765432"
allowed_user_ids = ["987654321098765432"]
message_content_intent = true   # only if you want free-text replies here
```

Token goes in `.env`:

```sh
cp .env.example "$(herdr plugin config-dir huketo.hitl)/.env"
chmod 600 "$(herdr plugin config-dir huketo.hitl)/.env"
$EDITOR "$(herdr plugin config-dir huketo.hitl)/.env"   # fill HITL_DISCORD_BOT_TOKEN
```

### `allowed_user_ids`

`channel_id`/`user_id` say *where* the question goes. `allowed_user_ids` says *who may answer*. Non-empty means a button click or reply from any other snowflake is rejected and the question stays open. In a shared channel, set it — otherwise any member can answer for you.

## 7. Verify

```sh
herdr-hitl daemon restart
herdr-hitl doctor
```

`doctor` opens the gateway session, reports the bot tag, and resolves the target channel or DM. It never prints the token.

## 8. Why there is a daemon

Discord rate-limits gateway `IDENTIFY` to **1000 per 24 hours per bot token**, and the penalty for exceeding it is not a retry-after — Discord **resets your bot token**, which takes the bot offline until you copy the new one out of the portal and reconfigure.

One process per `ask` would IDENTIFY once per question. A busy agent asking every few minutes crosses 1000 in well under a day and bricks the bot. So the daemon holds exactly one gateway session for its whole lifetime and every `ask` rides on it. See [ADR-0001](adr/0001-blocking-cli-over-a-resident-daemon.md).

## 9. Smoke test

```sh
herdr-hitl ask \
  -t "Setup smoke test" \
  -m "Click a button, or reply with any text." \
  -c "ok=Looks good" -c "nope=Something is off" \
  --primary ok --danger nope --timeout 5m
```

Click `Looks good`: the terminal prints `Looks good` and exits `0`, and the Discord message edits in place to show the outcome with the buttons disabled. Let the deadline pass instead and the command exits `3`.

## Troubleshooting

| Symptom | Cause |
| --- | --- |
| `50278 Cannot send a message to this user…` | No mutual guild. Invite the bot to a server you are in. |
| `50001 Missing Access` | Bot cannot see the channel. Re-invite with `permissions=125952`, or grant it in channel overrides. |
| `50013 Missing Permissions` | Channel overrides deny Send Messages, Embed Links, or Attach Files. |
| Buttons click but nothing happens | Interactions Endpoint URL is set. Clear it. |
| Free-text replies in a channel ignored | Message Content Intent off, or `message_content_intent = false`. Both must be on. |
| `Disallowed intent(s)` on connect | `message_content_intent = true` but the intent is not enabled in the portal. |
| Bot went offline and the token stopped working | IDENTIFY limit exceeded and Discord reset the token. Copy the new one; do not run more than one daemon per token. |
| Attachment rejected | Attachments are capped at 10 MiB and 10 per question. |
