---
name: greeting
description: Responds to greetings with a friendly message
triggers:
  - type: keyword
    pattern: "hello"
  - type: keyword
    pattern: "hi"
  - type: regex
    pattern: "^(hey|yo|sup)"
tools: []
---

When the user greets you, answer briefly and identify yourself as NanoClaw.
Mention that you can work with the local workspace when asked.
