# Webring Relay Service

This project is a webring relay service built with Go. It manages a list of websites, checks their uptime, and provides a dashboard for administration.

## Features

- Dashboard for managing websites in the webring
- Automatic uptime checking of websites (with proxy support)
- API endpoints for navigating the webring
- Telegram authentication and user management
- Site submission and update request workflow with admin approval
- Telegram approval polls: admins vote on each request, a majority decides it
- Ring integrity checks in a real browser, with a public health page and tier list
- Telegram notifications for status changes, submissions and approvals
- Customizable notification messages via template files
- Basic authentication for the dashboard

## Prerequisites

- Go 1.16 or later
- PostgreSQL database

## Installation

edit .env to set correct path to database
```
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
go mod tidy
cp .env.template .env
make migrate-up
```

## Local Run
```
go run cmd/server/main.go
```

or download prebuild version
```
wget https://github.com/Alexander-D-Karpov/webring/releases/latest/download/webring
chmod +x webring
./webring
```

## Customizing Notification Messages

Telegram notification templates live in the `messages/` directory (configurable via `MESSAGES_DIR` env var). 
Each file is plain text with Go template syntax and MarkdownV2 formatting. 

To customize a message, edit the corresponding `.txt` file. 

Available templates:

| File                        | Event                                       |
|-----------------------------|---------------------------------------------|
| `new_request_create.txt`    | Admin notification: new site submitted      |
| `new_request_update.txt`    | Admin notification: site update requested   |
| `approved_create.txt`       | User notification: site submission approved |
| `approved_update.txt`       | User notification: site update approved     |
| `declined_create.txt`       | User notification: site submission declined |
| `declined_update.txt`       | User notification: site update declined     |
| `admin_approved_create.txt` | Other admins: site creation approved        |
| `admin_approved_update.txt` | Other admins: site update approved          |
| `admin_declined_create.txt` | Other admins: site creation declined        |
| `admin_declined_update.txt` | Other admins: site update declined          |
| `site_online.txt`           | Owner notification: site back online        |
| `site_offline.txt`          | Owner notification: site went offline       |
| `poll_approved.txt`         | Request approved by admin vote              |
| `poll_declined.txt`         | Request declined by admin vote              |

## Telegram Approval Polls

When a request is submitted, the bot can post a poll to a shared admin group chat. Once a
majority of admins pick the same option the request is applied or discarded, the poll is
closed, and the list of admins who voted that way is posted to the group and sent to each
admin.

Set `TELEGRAM_ADMIN_CHAT_ID` to the group's chat ID to enable it. Leaving it empty keeps
the previous behavior, where requests are decided from the dashboard only.

Setup:

1. Create a group, add the bot to it, and add every admin.
2. Find the group's chat ID (it is negative) and put it in `TELEGRAM_ADMIN_CHAT_ID`.
3. Make sure each admin has a `telegram_id` in the `users` table. Admins without one
   cannot vote and still count towards the majority.

The threshold is a simple majority of admins who can vote — four out of seven — and is
fixed when the poll is created, so promoting an admin mid-vote does not change it. Only
votes from users flagged `is_admin` are counted; everyone else in the group is ignored.
The dashboard's approve and reject buttons keep working and close any open poll.

Each request on `/admin/requests` shows how its vote is going: the running count against
the threshold, and who voted which way. Requests submitted before polls were enabled
simply show no vote block.

Votes are read with `getUpdates` long polling, so the bot must not have a webhook set and
only one instance may run with a given token. A `409 Conflict` in the logs means one of
those two conditions is violated.

## Ring Integrity Checks

`cmd/ringcheck` loads every member in headless Chromium at four screen sizes and judges
whether the site is holding up its end of the ring. Each problem carries a severity and a
cost; the site gets a 0-100 score and an S-F tier. A perfect widget is deliberately hard
to build, so 100 is rare.

| Check | Severity | Cost | Meaning |
|---|---|---|---|
| `site_down` | critical | score 0 | the uptime checker cannot reach it, so no browser is launched |
| `render_failed` | critical | score 0 | navigation failed, timed out, or the body came back empty |
| `no_widget` | critical | 60 | no link to the ring and none to either neighbor |
| `wrong_slug` | major | 40 | the widget points at another member's endpoints, a copy-paste slip |
| `hidden` | major | 35 | ring links are in the DOM but render invisible |
| `stale_neighbors` | major | 25 | the only ring links point at members who are no longer neighbors |
| `broken_link` | major | 22 | the widget's ring endpoint answers with an error |
| `js_only` | major | 18 | with scripts off the page offers no way round the ring |
| `one_way` | major | 14 | the ring can only be walked in one direction |
| `below_fold` | minor | up to 34 | the reader has to scroll to reach the widget |
| `no_neighbor_name` | minor | 10 | the links say next and previous without saying who they are |
| `redirected` | minor | 10 | the site now answers on a different host than the one on record |
| `tiny_target` | minor | 6 | the links are under 24px across, too small to tap on a phone |
| `no_ring_link` | minor | 5 | nothing links back to the ring itself |
| `slow_render` | minor | 5 | the page takes over eight seconds to become readable |

`below_fold` is charged per screen of scrolling and weighted per size, so a widget lost on
a desktop costs more than one lost on a phone, where scrolling is expected anyway. However
poor a working widget is, it always scores above a member who never added one.

Tiers are S at 100, A from 88, B from 72, C from 55, D from 30, and F below that.

A member may wire the ring up either way: linking to `/{slug}/next` and `/{slug}/prev` on
the ring, or linking straight to the current neighbors. Both count, and a widget that
resolves its neighbors and names them scores better than a pair of bare arrows.

Widget links are found by the shape of their path — a member slug followed by `next`,
`prev` or `random` — rather than by domain. The ring answers on several hosts and is
mounted under a path on one of them, so there is nothing to configure and a widget
pointing at any of them is recognized.

The checker looks inside framesets and iframes, follows anchors wired through `onclick`
rather than `href`, and loads every page a second time with scripting switched off to see
what a reader without JavaScript is left with. A site may build one widget in script and
ship a different one in a `<noscript>` fallback; the two need not share a URL, and only
the scripts-off render can tell whether anything still carries the reader onward.

Results appear on two public pages. `/health` lists every member with its score and the
reasons behind it, reachable from the footer of the main page. `/tiers` is the S-F tier
list, reachable by direct URL.

The checker is a separate binary and image so the web server never has to carry a
browser; the two share the database, and the server only reads what the checker writes.

```
docker build -f Dockerfile.ringcheck -t webring-ringcheck .
docker run --env-file .env webring-ringcheck
```

Running it locally needs a Chromium on `PATH` or in `CHROME_PATH`. `ringcheck -once` runs
a single pass and exits, which is what you want when trying it out. Without a browser the
checker refuses to start and says so; the web server is unaffected and simply shows no
results yet.

On NixOS the checker runs from a systemd timer:

```nix
services.webring.ringCheck = {
  enable = true;
  interval = "6h";
};
```

## Usage

- Access the dashboard at `http://localhost:8080/dashboard` (use the credentials set in your `.env` file)
- API endpoints:
    - Next site: `GET /{slug}/next/data`
    - Previous site: `GET /{slug}/prev/data`
    - Random site: `GET /{slug}/random/data`
    - Full data for a site: `GET /{slug}/data`
- Redirect endpoints:
    - Visit site: `GET /{slug}`
    - Next site: `GET /{slug}/next`
    - Previous site: `GET /{slug}/prev`
    - Random site: `GET /{slug}/random`