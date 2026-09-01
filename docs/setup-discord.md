# Discord setup

*English · [한국어](setup-discord.ko.md)*

Fifteen minutes, one application, one private bot token. Questions arrive as messages with real Discord buttons; answers come back as button interactions or plain replies.

Questions arrive as a direct message from your bot, exactly as they do on Telegram. Discord adds one precondition that Telegram does not: **a bot may only DM a user it shares a server with**. Skipping it fails with `50278 Cannot send messages to this user due to having no mutual guilds`.

That server is plumbing, not a destination. No question is posted there and you never open it — an empty server holding just you and the bot is enough. Leave the server or remove the bot from it and DMs break again.

## 1. Create the application and bot

1. Open the [Discord developer portal](https://discord.com/developers/applications) and click **New Application**. Name it (e.g. `My HITL`).
2. Copy the **Application ID** from **General Information** — you need it for the invite URL.
3. Go to **Bot**. Click **Reset Token**, confirm, and copy the token. Discord shows it exactly once; if you lose it, reset again (which invalidates the old one).

## 2. Leave the Interactions Endpoint URL blank

Still under **General Information**, there is an **Interactions Endpoint URL** field. **Leave it empty.**

Setting it flips your application to HTTP interactions permanently: Discord stops delivering button clicks over the gateway and POSTs them to your URL instead, and it validates the URL with a signed PING before accepting it. `herdr-hitl` receives interactions over the gateway, so a configured endpoint diverts every button click away from the daemon and buttons appear to do nothing. If someone already set it, clear the field and save.

## 3. Make the bot private

Under **Bot**, turn **Public Bot** **off**.

A public bot can be added to any server by anyone who knows your Application ID, and that id is not a secret — it is the `client_id` in every invite URL. A HITL bot reads your questions and carries your answers; it has no reason to be joinable by strangers. With **Public Bot** off, Discord accepts the invite only from the application owner, so the same URL keeps working for you and fails for everybody else.

Leave **Requires OAuth2 Code Grant** **off** as well. It turns the invite into a full authorization-code exchange that needs a redirect URI and a server to receive the callback. `herdr-hitl` has neither, and the plain invite URL below stops working the moment it is enabled.

You do not need anything under **OAuth2 > Redirects** either. A bot-only invite carries no redirect.

| Setting | Value | Why |
| --- | --- | --- |
| Public Bot | **off** | Only you can add the bot to a server |
| Requires OAuth2 Code Grant | **off** | The plain invite URL fails without a callback server |
| Interactions Endpoint URL | **empty** | Keeps interactions on the gateway |
| Message Content Intent | **off** | See step 4 |
| OAuth2 redirect URI | none | Not used by a bot invite |

## 4. Message Content intent — almost always off

Under **Bot > Privileged Gateway Intents** there is **Message Content Intent**. Leave it **off**. You very probably do not need it.

Every question that allows free text carries a **Write answer…** button. Clicking it opens a Discord modal — a proper multi-line text box, up to 4000 characters — and the typed answer arrives inside the interaction. Interactions are not intent-gated, so this works in a DM and in a guild channel with no privileged intent at all.

The intent buys exactly one extra thing: answering by typing an ordinary message in the channel instead of using the button. Weigh it like this:

| Where the question goes | Buttons | Modal (Write answer…) | Plain message reply |
| --- | --- | --- | --- |
| DM with the bot | works | works | works — DMs are exempt from the restriction |
| Guild channel, intent **off** | works | works | ignored, content arrives empty |
| Guild channel, intent **on** | works | works | works |

Turn it on only if plain replies in a guild channel matter to you, and then set `message_content_intent = true` in the config so the daemon requests it. Enabling it in the portal without the config flag changes nothing; setting the config flag without enabling it in the portal closes the gateway with `Disallowed intent(s)`.

## 5. Invite the bot

Replace `<APP_ID>` with the Application ID from step 1 and open the URL:

```
https://discord.com/oauth2/authorize?client_id=<APP_ID>&scope=bot%20applications.commands&permissions=125952
```

`125952` is `VIEW_CHANNEL | SEND_MESSAGES | EMBED_LINKS | ATTACH_FILES | READ_MESSAGE_HISTORY` — the minimum for posting a question with attachments and editing it afterwards to show the outcome.

With **Public Bot** off this URL works only while you are signed in as the application owner; for anyone else Discord shows "This bot cannot be added to servers". That is the point. The portal's **OAuth2 > URL Generator** builds the same URL from checkboxes if you would rather not hand-write it — tick `bot` and `applications.commands`, then the five permissions above.

Pin the invite to one server so a stray click cannot add the bot elsewhere:

```
https://discord.com/oauth2/authorize?client_id=<APP_ID>&scope=bot%20applications.commands&permissions=125952&guild_id=<GUILD_ID>&disable_guild_select=true
```

You must invite the bot to a guild you share **even if you only want DMs**. Discord refuses a bot-to-user DM when there is no mutual guild, with `50278: Cannot send messages to this user due to having no mutual guilds`. A private server with just you and the bot is enough, and `herdr-hitl` puts this invite URL directly in the error when it hits that code.

## 6. Copy a channel or user id

Enable **Settings > Advanced > Developer Mode** in the Discord client. Right-click any channel and choose **Copy Channel ID**; right-click yourself and choose **Copy User ID**. Both are 17–19 digit snowflakes.

## 7. Write the config

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
cp .env.example "$(dirname "$(herdr-hitl config path)")/.env"
chmod 600 "$(dirname "$(herdr-hitl config path)")/.env"
$EDITOR "$(dirname "$(herdr-hitl config path)")/.env"   # fill HITL_DISCORD_BOT_TOKEN
```

### `allowed_user_ids`

`channel_id`/`user_id` say *where* the question goes. `allowed_user_ids` says *who may answer*. Non-empty means a button click or reply from any other snowflake is rejected and the question stays open. In a shared channel, set it — otherwise any member can answer for you.

## 8. Verify

```sh
herdr-hitl daemon restart
herdr-hitl doctor
```

`doctor` opens the gateway session, reports the bot tag, and resolves the target channel or DM. It never prints the token.

## 9. Why there is a daemon

Discord rate-limits gateway `IDENTIFY` to **1000 per 24 hours per bot token**, and the penalty for exceeding it is not a retry-after — Discord **resets your bot token**, which takes the bot offline until you copy the new one out of the portal and reconfigure.

One process per `ask` would IDENTIFY once per question. A busy agent asking every few minutes crosses 1000 in well under a day and bricks the bot. So the daemon holds exactly one gateway session for its whole lifetime and every `ask` rides on it. See [ADR-0001](adr/0001-blocking-cli-over-a-resident-daemon.md).

## 10. Smoke test

```sh
herdr-hitl ask \
  -t "Setup smoke test" \
  -m "Click a button, or reply with any text." \
  -c "ok=Looks good" -c "nope=Something is off" \
  --primary ok --danger nope --timeout 5m
```

Click `Looks good`: the terminal prints `Looks good` and exits `0`, and the Discord message edits in place to show the outcome with the buttons removed. Click **Write answer…** instead and a modal opens; whatever you type comes back as the answer. Let the deadline pass and the command exits `3`.

## Troubleshooting

| Symptom | Cause |
| --- | --- |
| `50278 Cannot send a message to this user…` | No mutual guild. Invite the bot to a server you are in. |
| `50001 Missing Access` | Bot cannot see the channel. Re-invite with `permissions=125952`, or grant it in channel overrides. |
| `50013 Missing Permissions` | Channel overrides deny Send Messages, Embed Links, or Attach Files. |
| Buttons click but nothing happens | Interactions Endpoint URL is set. Clear it. |
| `This bot cannot be added to servers` on the invite URL | **Public Bot** is off and you are not signed in as the application owner. That is the intended behaviour — sign in as the owner. |
| Invite URL redirects to an error page or asks for a redirect URI | **Requires OAuth2 Code Grant** is on. Turn it off. |
| Plain message replies in a channel ignored | Message Content Intent off, or `message_content_intent = false`. Both must be on — or just use the **Write answer…** button, which needs neither. |
| `Disallowed intent(s)` on connect | `message_content_intent = true` but the intent is not enabled in the portal. |
| Bot went offline and the token stopped working | IDENTIFY limit exceeded and Discord reset the token. Copy the new one; do not run more than one daemon per token. |
| Attachment rejected | Attachments are capped at 10 MiB and 10 per question. |
